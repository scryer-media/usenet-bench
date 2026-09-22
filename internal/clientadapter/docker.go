package clientadapter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

type dockerClient struct {
	binary string
}

type runningContainer struct {
	docker    dockerClient
	name      string
	endpoint  string
	configDir string
}

func startContainer(ctx context.Context, cfg Config, spec ProductSpec) (*runningContainer, error) {
	downloadsDir, incompleteDir := hostDownloadDirs(cfg)
	if err := os.MkdirAll(incompleteDir, 0o755); err != nil {
		return nil, fmt.Errorf("create incomplete directory: %w", err)
	}
	for _, path := range []string{cfg.ConfigDir, cfg.OutputDir, downloadsDir, incompleteDir} {
		if strings.Contains(path, ",") {
			return nil, fmt.Errorf("Docker bind path must not contain a comma: %s", path)
		}
	}
	downloadMounts := downloadMounts(cfg, downloadsDir, incompleteDir)
	if spec.NeedsNZBMount && strings.Contains(cfg.NZBPath, ",") {
		return nil, fmt.Errorf("Docker NZB bind path must not contain a comma: %s", cfg.NZBPath)
	}
	name := containerNameFor(cfg.RunID, string(cfg.Client))
	docker := dockerClient{binary: cfg.DockerBinary}
	args := []string{
		"run", "--detach",
		"--name", name,
		"--network", cfg.Network,
		"--label", "com.scryer-media.weaver.nntp-bench.run=" + cfg.RunID,
		"--mount", mount(cfg.ConfigDir, "/config", false),
	}
	for _, downloadMount := range downloadMounts {
		args = append(args, "--mount", downloadMount)
	}
	if spec.ExposeAPI {
		if spec.APIPort < 1 {
			return nil, fmt.Errorf("%s API port must be positive when an API is exposed", cfg.Client)
		}
		args = append(args, "--publish", "127.0.0.1::"+strconv.Itoa(spec.APIPort))
	}
	if spec.NeedsNZBMount {
		args = append(args, "--mount", mount(cfg.NZBPath, "/benchmark-input/"+filepath.Base(cfg.NZBPath), true))
	}
	if spec.NeedsCAMount {
		if strings.Contains(cfg.NNTPCAFile, ",") {
			return nil, fmt.Errorf("Docker CA bind path must not contain a comma: %s", cfg.NNTPCAFile)
		}
		args = append(args, "--mount", mount(cfg.NNTPCAFile, "/benchmark-ca/nntp-ca.pem", true))
	}
	if cfg.Platform != "" {
		args = append(args, "--platform", cfg.Platform)
	}
	for _, variable := range spec.Environment {
		args = append(args, "--env", variable)
	}
	args = append(args, cfg.Image)
	args = append(args, spec.Command...)
	if _, err := docker.run(ctx, args...); err != nil {
		return nil, fmt.Errorf("start %s container: %w", cfg.Client, err)
	}
	return &runningContainer{docker: docker, name: name, configDir: cfg.ConfigDir}, nil
}

// resolveEndpoint is intentionally separate from startContainer. The runner
// starts the container-scoped counters in the interval between these calls, so
// port inspection never creates a product-specific startup accounting gap.
func (container *runningContainer) resolveEndpoint(ctx context.Context, containerPort int) error {
	endpoint, err := container.docker.publishedEndpoint(ctx, container.name, containerPort)
	if err != nil {
		return err
	}
	container.endpoint = endpoint
	return nil
}

// hostDownloadDirs returns the host directory that stands in for the
// container's /downloads and the intermediate directory inside it. The
// completion directory is the run's OutputDir; the intermediate directory is
// its sibling, so both live on one host filesystem.
func hostDownloadDirs(cfg Config) (downloadsDir, incompleteDir string) {
	downloadsDir = filepath.Dir(cfg.OutputDir)
	return downloadsDir, filepath.Join(downloadsDir, "incomplete")
}

// downloadMounts places the client's intermediate and completion directories
// according to the storage profile.
//
// Local storage is ONE bind of the host downloads directory at /downloads, so
// /downloads/incomplete and /downloads/complete are two directories on the
// same filesystem inside the container and every client's final move is a
// rename, exactly as on a real single-disk install. Two separate binds would
// be two mounts to the kernel: rename(2) fails with EXDEV and each client
// falls back to a full copy of the extracted output, adding a write of the
// whole payload that no user's machine performs.
//
// The NFS profiles are the opposite by design: the completion directory (and
// for nfs-all the intermediate one too) is a volume on the export, so the
// final move IS a copy across the network, which is the cost those profiles
// exist to measure.
func downloadMounts(cfg Config, downloadsDir, incompleteDir string) []string {
	if cfg.IncompleteVolume == "" && cfg.CompleteVolume == "" {
		return []string{mount(downloadsDir, "/downloads", false)}
	}
	incomplete := mount(incompleteDir, "/downloads/incomplete", false)
	if cfg.IncompleteVolume != "" {
		incomplete = volumeMount(cfg.IncompleteVolume, "/downloads/incomplete")
	}
	complete := mount(cfg.OutputDir, containerCompletionDir, false)
	if cfg.CompleteVolume != "" {
		complete = volumeMount(cfg.CompleteVolume, containerCompletionDir)
	}
	return []string{incomplete, complete}
}

// containerCompletionDir is where every client's finished output lands inside
// its container. It is the directory whose mount decides whether the final
// move is a rename or a copy, so it is the one the parity block records.
const containerCompletionDir = "/downloads/complete"

func volumeMount(name, destination string) string {
	return "type=volume,src=" + name + ",dst=" + destination
}

func mount(source, destination string, readOnly bool) string {
	value := "type=bind,src=" + source + ",dst=" + destination
	if readOnly {
		value += ",readonly"
	}
	return value
}

func containerNameFor(runID string, client string) string {
	var builder strings.Builder
	for _, character := range runID + "-" + client {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
		default:
			builder.WriteByte('-')
		}
	}
	base := strings.Trim(builder.String(), "-")
	if base == "" {
		base = "run"
	}
	if len(base) > 44 {
		base = base[:44]
	}
	seed := fmt.Sprintf("%s:%d", base, time.Now().UnixNano())
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("nntpbench-%s-%x", base, digest[:4])
}

func (d dockerClient) run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, d.binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		preview := strings.TrimSpace(string(output))
		if len(preview) > 2_000 {
			preview = preview[:2_000] + "…"
		}
		if preview == "" {
			return "", fmt.Errorf("Docker command failed: %w", err)
		}
		return "", fmt.Errorf("Docker command failed: %w: %s", err, preview)
	}
	return strings.TrimSpace(string(output)), nil
}

func (d dockerClient) publishedEndpoint(ctx context.Context, name string, containerPort int) (string, error) {
	output, err := d.run(ctx, "port", name, strconv.Itoa(containerPort)+"/tcp")
	if err != nil {
		return "", fmt.Errorf("inspect published client API port: %w", err)
	}
	return endpointFromDockerPort(output, name)
}

func endpointFromDockerPort(output, name string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		address := strings.TrimSpace(line)
		if address == "" {
			continue
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			continue
		}
		switch host {
		case "0.0.0.0", "":
			host = "127.0.0.1"
		case "::":
			host = "::1"
		}
		return "http://" + net.JoinHostPort(host, port), nil
	}
	return "", fmt.Errorf("Docker did not return a usable published port for %s", name)
}

func (d dockerClient) containerPID(ctx context.Context, name string) (int, error) {
	output, err := d.run(ctx, "inspect", "--format", "{{.State.Pid}}", name)
	if err != nil {
		return 0, fmt.Errorf("inspect client container PID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil || pid < 1 {
		return 0, fmt.Errorf("client container has no usable host PID")
	}
	return pid, nil
}

func (d dockerClient) containerCgroup(ctx context.Context, name string) (string, error) {
	return d.containerControllerCgroup(ctx, name, "perf_event")
}

func (d dockerClient) containerControllerCgroup(ctx context.Context, name, controller string) (string, error) {
	pid, err := d.containerPID(ctx, name)
	if err != nil {
		return "", err
	}
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", fmt.Errorf("read client container cgroup: %w", err)
	}
	cgroup, err := parseControllerCgroup(string(contents), controller)
	if err != nil {
		return "", fmt.Errorf("parse client container cgroup: %w", err)
	}
	// A remote daemon's PID may exist locally but belong to another process.
	// Require the exact inspected container ID in the local cgroup path.
	id, err := d.run(ctx, "inspect", "--format", "{{.Id}}", name)
	if err != nil {
		return "", fmt.Errorf("bind cgroup to container identity: %w", err)
	}
	if err := validateContainerCgroupIdentity(cgroup, strings.TrimSpace(id)); err != nil {
		return "", err
	}
	return cgroup, nil
}

func parseContainerCgroup(contents string) (string, error) {
	return parseControllerCgroup(contents, "perf_event")
}

func validateContainerCgroupIdentity(group, id string) error {
	if len(id) != 64 || !filepath.IsLocal(group) {
		return fmt.Errorf("container identity or cgroup path is invalid")
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return fmt.Errorf("invalid container ID")
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(group), "/") {
		if part == id || part == "docker-"+id+".scope" {
			return nil
		}
	}
	return fmt.Errorf("local cgroup does not belong to the inspected container; remote/namespaced CPU accounting is unavailable")
}

func parseControllerCgroup(contents, wanted string) (string, error) {
	var perfEvent string
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(fields) != 3 {
			continue
		}
		path := strings.TrimPrefix(strings.TrimSpace(fields[2]), "/")
		if path == "" {
			continue
		}
		if fields[0] == "0" && fields[1] == "" {
			return path, nil
		}
		for _, controller := range strings.Split(fields[1], ",") {
			if controller == wanted {
				perfEvent = path
			}
		}
	}
	if perfEvent != "" {
		return perfEvent, nil
	}
	return "", fmt.Errorf("no non-root cgroup v2 or %s path found", wanted)
}

func (d dockerClient) containerRunning(ctx context.Context, name string) (bool, error) {
	output, err := d.run(ctx, "inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return false, fmt.Errorf("inspect client container state: %w", err)
	}
	running, err := strconv.ParseBool(strings.TrimSpace(output))
	if err != nil {
		return false, fmt.Errorf("parse client container running state %q: %w", output, err)
	}
	return running, nil
}

func (d dockerClient) imageVersion(ctx context.Context, image string) string {
	for _, label := range []string{"org.opencontainers.image.version", "build_version"} {
		output, err := d.run(ctx, "image", "inspect", "--format", "{{index .Config.Labels \""+label+"\"}}", image)
		if err == nil {
			value := strings.TrimSpace(output)
			if value != "" && value != "<no value>" {
				return value
			}
		}
	}
	return image
}

func (container *runningContainer) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := container.docker.run(ctx, "logs", container.name); err == nil {
		// Product entrypoints may chown /config to their runtime UID. The suite
		// directory itself is never mounted, so it remains writable by the
		// benchmark controller even after a failed container startup.
		path := filepath.Join(filepath.Dir(container.configDir), "client-container.log")
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if createErr == nil {
			_, _ = file.WriteString(output + "\n")
			_ = file.Close()
		}
	}
	_, _ = container.docker.run(ctx, "rm", "--force", container.name)
}

// dockerInspectRuntime is the shape `docker inspect` is asked for. Only the
// fields the report states are decoded: the point is to record what ran, not
// to snapshot the daemon.
type dockerInspectRuntime struct {
	ID     string `json:"Id"`
	Image  string `json:"Image"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	HostConfig struct {
		NanoCPUs    int64  `json:"NanoCpus"`
		CPUQuota    int64  `json:"CpuQuota"`
		CPUPeriod   int64  `json:"CpuPeriod"`
		Memory      int64  `json:"Memory"`
		PidsLimit   *int64 `json:"PidsLimit"`
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

// inspectRuntime reads back what the daemon actually gave the container. It
// is deliberately taken after the container is running rather than assembled
// from the flags the harness passed: a daemon default, a cgroup driver that
// cannot honour a limit, or a compose-level override all leave the intended
// configuration intact while changing what ran, and a parity claim has to be
// about what ran.
//
// workingDir is the container path whose mount decides whether the client's
// final move is a rename or a copy; the mount carrying it is the one
// recorded.
func (d dockerClient) inspectRuntime(ctx context.Context, name, workingDir string) benchmark.ContainerRuntime {
	output, err := d.run(ctx, "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return benchmark.ContainerRuntime{Unavailable: "inspect client container runtime: " + err.Error()}
	}
	var inspected dockerInspectRuntime
	if err := json.Unmarshal([]byte(output), &inspected); err != nil {
		return benchmark.ContainerRuntime{Unavailable: "decode client container runtime: " + err.Error()}
	}
	if strings.TrimSpace(inspected.ID) == "" {
		return benchmark.ContainerRuntime{Unavailable: "client container runtime readback carried no container id"}
	}
	runtime := benchmark.ContainerRuntime{
		Inspected:        true,
		ContainerID:      inspected.ID,
		Image:            inspected.Config.Image,
		ImageDigest:      inspected.Image,
		NanoCPUs:         inspected.HostConfig.NanoCPUs,
		CPUQuotaMicros:   inspected.HostConfig.CPUQuota,
		CPUPeriodMicros:  inspected.HostConfig.CPUPeriod,
		MemoryLimitBytes: inspected.HostConfig.Memory,
		NetworkMode:      inspected.HostConfig.NetworkMode,
	}
	if inspected.HostConfig.PidsLimit != nil {
		runtime.PidsLimit = *inspected.HostConfig.PidsLimit
	}
	best := ""
	for _, mount := range inspected.Mounts {
		destination := strings.TrimSuffix(mount.Destination, "/")
		if destination != workingDir && !strings.HasPrefix(workingDir, destination+"/") {
			continue
		}
		// The longest matching destination is the mount the directory
		// actually lives on: /downloads/complete wins over /downloads.
		if len(destination) <= len(best) {
			continue
		}
		best = destination
		source := mount.Source
		if mount.Type == "volume" && strings.TrimSpace(mount.Name) != "" {
			source = mount.Name
		}
		runtime.WorkingDirMountType = mount.Type
		runtime.WorkingDirMountSource = source
		runtime.WorkingDirMountDestination = mount.Destination
	}
	return runtime
}
