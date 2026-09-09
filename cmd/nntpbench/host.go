package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type hostFact struct {
	Value       string `json:"value,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
}

// These are read-only observations, not host tuning or cache manipulation.
func captureHostFacts() map[string]hostFact {
	facts := map[string]hostFact{}
	command := func(key, name string, args ...string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		raw, err := exec.CommandContext(ctx, name, args...).Output()
		if err != nil {
			facts[key] = hostFact{Unavailable: err.Error()}
			return
		}
		if len(raw) > 16384 {
			raw = raw[:16384]
		}
		facts[key] = hostFact{Value: strings.TrimSpace(string(raw))}
	}
	file := func(key, path string) {
		raw, err := os.ReadFile(path)
		if err != nil {
			facts[key] = hostFact{Unavailable: err.Error()}
			return
		}
		if len(raw) > 16384 {
			raw = raw[:16384]
		}
		facts[key] = hostFact{Value: strings.TrimSpace(string(raw))}
	}
	switch runtime.GOOS {
	case "darwin":
		command("cpu_model", "sysctl", "-n", "machdep.cpu.brand_string")
		command("memory_bytes", "sysctl", "-n", "hw.memsize")
		command("kernel", "uname", "-srv")
		command("power_policy", "pmset", "-g", "custom")
		command("load_at_capture", "sysctl", "-n", "vm.loadavg")
	case "linux":
		file("cpu", "/proc/cpuinfo")
		file("memory", "/proc/meminfo")
		file("load_at_capture", "/proc/loadavg")
		file("process_limits_affinity", "/proc/self/status")
		command("kernel", "uname", "-srv")
	case "windows":
		command("hardware", "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-CimInstance Win32_Processor | Select-Object Name,NumberOfCores,NumberOfLogicalProcessors | ConvertTo-Json -Compress")
		command("power_policy", "powercfg.exe", "/GETACTIVESCHEME")
	default:
		facts["hardware"] = hostFact{Unavailable: "platform collector unavailable"}
	}
	return facts
}
