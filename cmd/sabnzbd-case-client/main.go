//go:build !windows

// sabnzbd-case-client adapts SABnzbd's local HTTP API to the one-shot client
// contract used by adversarial-supervise. It is an observer-side launcher, not
// part of the client whose behavior is being evaluated.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	sabPython = "/lsiopy/bin/python3"
	sabMain   = "/app/sabnzbd/SABnzbd.py"
	apiKey    = "adversarial-supervisor-only"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && args[0] == "--version" {
		version, err := sabVersion()
		if err != nil {
			return err
		}
		fmt.Println(version)
		return nil
	}
	if len(args) > 0 && args[0] == "restart-loop" {
		return restartLoop(args[1:])
	}
	configDir, input, report, err := parseOneShotArgs(args)
	if err != nil {
		return err
	}
	return download(configDir, input, report)
}

func parseOneShotArgs(args []string) (configDir, input, report string, err error) {
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--config":
			index++
			if index >= len(args) {
				return "", "", "", fmt.Errorf("--config requires a value")
			}
			configDir = args[index]
		case "download":
			index++
			if index >= len(args) {
				return "", "", "", fmt.Errorf("download requires an NZB path")
			}
			input = args[index]
		case "--report":
			index++
			if index >= len(args) {
				return "", "", "", fmt.Errorf("--report requires a value")
			}
			report = args[index]
		default:
			return "", "", "", fmt.Errorf("unexpected one-shot argument %q", args[index])
		}
	}
	if configDir == "" || input == "" || report == "" {
		return "", "", "", fmt.Errorf("expected --config DIR download INPUT --report FILE")
	}
	return configDir, input, report, nil
}

func sabVersion() (string, error) {
	raw, err := exec.Command(sabPython, sabMain, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read SABnzbd version: %w: %s", err, bytes.TrimSpace(raw))
	}
	for _, field := range strings.Fields(string(raw)) {
		if strings.HasPrefix(field, "SABnzbd.py-") {
			return field, nil
		}
	}
	return "", fmt.Errorf("SABnzbd version output was not recognized")
}

func download(configDir, input, report string) error {
	port, err := availablePort()
	if err != nil {
		return err
	}
	ini, err := renderConfig(port)
	if err != nil {
		return err
	}
	configPath := filepath.Join(configDir, "sabnzbd.ini")
	if err := os.WriteFile(configPath, []byte(ini), 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, sabPython, sabMain,
		"--config-file", configPath,
		"--server", "127.0.0.1:"+strconv.Itoa(port),
		"--browser", "0", "--console", "--new",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/api"
	if err := waitReady(client, baseURL, done); err != nil {
		return err
	}
	nzoID, err := submit(client, baseURL, input)
	if err != nil {
		return err
	}
	if err := waitTerminal(client, baseURL, nzoID, done); err != nil {
		return err
	}
	terminal, _ := json.Marshal(map[string]any{"schema_version": 1, "status": "complete"})
	if err := os.WriteFile(report, append(terminal, '\n'), 0600); err != nil {
		return err
	}
	return nil
}

func availablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func renderConfig(port int) (string, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", fmt.Errorf("missing %s", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("invalid %s", name)
		}
		return value, nil
	}
	downloadDir, err := required("WEAVER_INTERMEDIATE_DIR")
	if err != nil {
		return "", err
	}
	completeDir, err := required("WEAVER_COMPLETE_DIR")
	if err != nil {
		return "", err
	}
	host, err := required("WEAVER_SERVER_1_HOSTNAME")
	if err != nil {
		return "", err
	}
	nntpPort, err := required("WEAVER_SERVER_1_PORT")
	if err != nil {
		return "", err
	}
	username, err := required("WEAVER_SERVER_1_USERNAME")
	if err != nil {
		return "", err
	}
	password, err := required("WEAVER_SERVER_1_PASSWORD")
	if err != nil {
		return "", err
	}
	connections, err := required("WEAVER_SERVER_1_CONNECTIONS")
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"[misc]",
		"host = 127.0.0.1",
		"port = " + strconv.Itoa(port),
		"api_key = " + apiKey,
		"download_dir = " + downloadDir,
		"complete_dir = " + completeDir,
		"enable_unrar = 1",
		"direct_unpack = 1",
		"direct_unpack_tested = 1",
		"deobfuscate_final_filenames = 0",
		"pre_check = 0",
		"pause_on_post_processing = 0",
		"config_conversion_version = 5",
		"",
		"[servers]",
		"[[benchmark]]",
		"host = " + host,
		"port = " + nntpPort,
		"username = " + username,
		"password = " + password,
		"connections = " + connections,
		"pipelining_requests = 2",
		"ssl = 0",
		"ssl_verify = 0",
		"",
	}, "\n"), nil
}

func waitReady(client *http.Client, baseURL string, done <-chan error) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return fmt.Errorf("SABnzbd exited during startup: %w", err)
		default:
		}
		var response struct {
			Version string `json:"version"`
		}
		if err := getJSON(client, apiURL(baseURL, "version", nil), &response); err == nil && response.Version != "" {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("SABnzbd API did not become ready")
}

func submit(client *http.Client, baseURL, input string) (string, error) {
	file, err := os.Open(input)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("name", filepath.Base(input))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	request, err := http.NewRequest(http.MethodPost, apiURL(baseURL, "addfile", nil), &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	var response struct {
		Status bool     `json:"status"`
		IDs    []string `json:"nzo_ids"`
	}
	if err := doJSON(client, request, &response); err != nil {
		return "", err
	}
	if !response.Status || len(response.IDs) != 1 || response.IDs[0] == "" {
		return "", fmt.Errorf("SABnzbd rejected the NZB submission")
	}
	return response.IDs[0], nil
}

func waitTerminal(client *http.Client, baseURL, nzoID string, done <-chan error) error {
	for {
		select {
		case err := <-done:
			return fmt.Errorf("SABnzbd exited before a terminal job state: %w", err)
		case <-time.After(100 * time.Millisecond):
		}
		values := url.Values{"limit": {"100"}}
		var response struct {
			History struct {
				Slots []map[string]any `json:"slots"`
			} `json:"history"`
		}
		if err := getJSON(client, apiURL(baseURL, "history", values), &response); err != nil {
			continue
		}
		for _, slot := range response.History.Slots {
			if fmt.Sprint(slot["nzo_id"]) != nzoID {
				continue
			}
			status := strings.ToLower(fmt.Sprint(slot["status"]))
			switch {
			case strings.Contains(status, "complete"), strings.Contains(status, "success"):
				return nil
			case strings.Contains(status, "fail"), strings.Contains(status, "delete"), strings.Contains(status, "abort"):
				return fmt.Errorf("SABnzbd terminal job status %q", slot["status"])
			}
		}
	}
}

func apiURL(baseURL, mode string, extra url.Values) string {
	values := url.Values{"mode": {mode}, "output": {"json"}, "apikey": {apiKey}}
	for key, entries := range extra {
		values[key] = append([]string(nil), entries...)
	}
	return baseURL + "?" + values.Encode()
}

func getJSON(client *http.Client, endpoint string, target any) error {
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	return doJSON(client, request, target)
}

func doJSON(client *http.Client, request *http.Request, target any) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("SABnzbd API returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("SABnzbd API returned trailing JSON")
	}
	return nil
}
