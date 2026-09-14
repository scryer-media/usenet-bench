//go:build windows

package nativeadapter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

func configureNativeProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000004} // CREATE_SUSPENDED
}

// The primary thread cannot start helpers until the accounting job owns it.
// Toolhelp supplies the thread handle that os/exec intentionally does not expose.
func resumeAccountedProcess(process *os.Process) error {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	snapshot, _, err := kernel.NewProc("CreateToolhelp32Snapshot").Call(4, 0) // TH32CS_SNAPTHREAD
	if syscall.Handle(snapshot) == syscall.InvalidHandle {
		return fmt.Errorf("thread snapshot: %v", err)
	}
	defer syscall.CloseHandle(syscall.Handle(snapshot))
	type threadEntry struct {
		Size, Usage, ID, Owner      uint32
		BasePriority, DeltaPriority int32
		Flags                       uint32
	}
	entry := threadEntry{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	ok, _, _ := kernel.NewProc("Thread32First").Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.Owner == uint32(process.Pid) {
			h, _, err := kernel.NewProc("OpenThread").Call(2, 0, uintptr(entry.ID))
			if h == 0 {
				return fmt.Errorf("open suspended primary thread: %v", err)
			}
			previous, _, err := kernel.NewProc("ResumeThread").Call(h)
			_ = syscall.CloseHandle(syscall.Handle(h))
			if previous != 1 {
				return fmt.Errorf("resume primary thread: count=%d error=%v", previous, err)
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, _ = kernel.NewProc("Thread32Next").Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}
	return fmt.Errorf("suspended primary thread not found")
}

// taskkill /T is the Windows process-tree boundary. Native SABnzbd and NZBGet
// are required to run in the foreground, but any helper they leave behind
// must still be removed before the next isolated benchmark run.
func interruptNativeProcessTree(process *os.Process) error {
	return terminateWindowsProcessTree(process)
}

func killNativeProcessTree(process *os.Process) error {
	return terminateWindowsProcessTree(process)
}

func terminateWindowsProcessTree(process *os.Process) error {
	if process == nil || process.Pid < 1 {
		return os.ErrProcessDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(process.Pid), "/T", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("taskkill /T /F: %w: %s", err, output)
	}
	return nil
}
