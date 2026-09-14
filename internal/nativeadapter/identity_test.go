package nativeadapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

func identityFile(t *testing.T, path, data string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeEnvironmentWindowsOverrideAndInjection(t *testing.T) {
	lookup := func(key string) (string, bool) { return "ambient", true }
	env := nativeEnvironmentForOS("windows", lookup, []string{"Path=explicit", "CUSTOM=kept"})
	paths := 0
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			paths++
			if e != "PATH=explicit" {
				t.Fatal(e)
			}
		}
		if strings.HasPrefix(e, "PYTHONPATH=") {
			t.Fatal("ambient injection included")
		}
	}
	if paths != 1 {
		t.Fatal(env)
	}
}

func TestNativeIdentityStableAcrossRunDirectories(t *testing.T) {
	root := t.TempDir()
	program := identityFile(t, filepath.Join(root, executableName(runtime.GOOS, "client")), "program", 0700)
	snapshots := []softwareSnapshot{}
	for _, run := range []string{"first", "second"} {
		cfg := Config{ConfigDir: filepath.Join(root, run, "config"), OutputDir: filepath.Join(root, run, "output")}
		spec := productSpec{Command: []string{program}, Environment: []string{"PATH=", "CLIENT_DATA=" + cfg.ConfigDir, "CLIENT_OUTPUT=" + cfg.OutputDir}}
		snapshot, err := snapshotNativeSoftware(cfg, spec)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
		t.Fatalf("run paths changed software identity: %#v / %#v", snapshots[0], snapshots[1])
	}
}

func TestNativeIdentityBindsBundleSourceAndHelper(t *testing.T) {
	root := t.TempDir()
	program := identityFile(t, filepath.Join(root, "Client.app", "Contents", "MacOS", executableName(runtime.GOOS, "client")), "program", 0700)
	helper := identityFile(t, filepath.Join(filepath.Dir(program), executableName(runtime.GOOS, "unrar")), "helper v1", 0700)
	source := identityFile(t, filepath.Join(root, "source", "client.py"), "print('v1')", 0600)
	resource := identityFile(t, filepath.Join(root, "Client.app", "Contents", "Resources", "module.dat"), "module v1", 0600)
	cfg := Config{LaunchCommand: []string{program, source}, ConfigDir: filepath.Join(root, "config")}
	spec := productSpec{Command: cfg.LaunchCommand, Environment: []string{"PATH="}}
	before, err := snapshotNativeSoftware(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{source, helper, resource} {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		if before.Sources[resolved] == "" {
			t.Fatalf("missing %s", p)
		}
	}
	identityFile(t, source, "print('v2')", 0600)
	after, err := snapshotNativeSoftware(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	if benchmark.EvidenceDigest(before) == benchmark.EvidenceDigest(after) {
		t.Fatal("source change ignored")
	}
	identityFile(t, helper, "helper v2", 0700)
	updated, err := snapshotNativeSoftware(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(after.Helpers, updated.Helpers) {
		t.Fatal("helper change ignored")
	}
	if err := os.MkdirAll(cfg.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	digest, _, err := nativeSoftwareIdentity(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "software-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved softwareSnapshot
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:"+benchmark.EvidenceDigest(saved) {
		t.Fatal("saved inventory does not bind client identity")
	}
}

func TestInterpretedClientRequiresDeclaredRuntime(t *testing.T) {
	root := t.TempDir()
	program := identityFile(t, filepath.Join(root, executableName(runtime.GOOS, "python3")), "interpreter", 0700)
	spec := productSpec{Command: []string{program}, Environment: []string{"PATH="}}
	if _, err := snapshotNativeSoftware(Config{}, spec); err == nil {
		t.Fatal("interpreter accepted without runtime declaration")
	}
	cfg := Config{IdentityPaths: []string{root}}
	before, err := snapshotNativeSoftware(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	identityFile(t, filepath.Join(root, "lib", "dependency.py"), "dependency", 0600)
	after, err := snapshotNativeSoftware(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	if benchmark.EvidenceDigest(before) == benchmark.EvidenceDigest(after) {
		t.Fatal("runtime change ignored")
	}
}

func TestIdentityRootsRejectRelativeAndFilesystemRoots(t *testing.T) {
	for _, p := range []string{"", "relative", filepath.VolumeName(t.TempDir()) + string(filepath.Separator)} {
		if err := validateIdentityPaths([]string{p}); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
}

func TestIdentitySymlinkTargetBoundAndDirectoryCycleTerminates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires symlink privilege")
	}
	root := t.TempDir()
	program := identityFile(t, filepath.Join(root, "app", "client"), "program", 0700)
	target := identityFile(t, filepath.Join(root, "runtime", "module"), "v1", 0600)
	if err := os.Symlink(filepath.Dir(target), filepath.Join(root, "app", "runtime")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "app"), filepath.Join(root, "app", "cycle")); err != nil {
		t.Fatal(err)
	}
	cfg := Config{IdentityPaths: []string{filepath.Join(root, "app")}}
	spec := productSpec{Command: []string{program}, Environment: []string{"PATH="}}
	before, err := snapshotNativeSoftware(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	identityFile(t, target, "v2", 0600)
	after, err := snapshotNativeSoftware(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Links) == 0 || benchmark.EvidenceDigest(before) == benchmark.EvidenceDigest(after) {
		t.Fatal("symlink target not bound")
	}
}

func TestHelperResolutionUsesEffectivePathAndBundlePriority(t *testing.T) {
	root := t.TempDir()
	program := identityFile(t, filepath.Join(root, "app", executableName(runtime.GOOS, "client")), "program", 0700)
	helper := identityFile(t, filepath.Join(root, "tools", executableName(runtime.GOOS, "unrar")), "path helper", 0700)
	env := []string{"PATH=" + filepath.Dir(helper)}
	got, err := resolveIdentityHelper(program, "unrar", env)
	if err != nil || got != helper {
		t.Fatalf("%s %v", got, err)
	}
	bundled := identityFile(t, filepath.Join(filepath.Dir(program), executableName(runtime.GOOS, "unrar")), "bundled helper", 0700)
	got, err = resolveIdentityHelper(program, "unrar", env)
	if err != nil || got != bundled {
		t.Fatalf("%s %v", got, err)
	}
	spec := productSpec{Command: []string{program}, Environment: env}
	before, err := snapshotNativeSoftware(Config{}, spec)
	if err != nil {
		t.Fatal(err)
	}
	identityFile(t, helper, "changed PATH helper", 0700)
	after, err := snapshotNativeSoftware(Config{}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if before.Helpers["unrar-path"] == after.Helpers["unrar-path"] || before.Helpers["unrar"] != after.Helpers["unrar"] {
		t.Fatal("bundle and PATH helper candidates were not independently bound")
	}
}
