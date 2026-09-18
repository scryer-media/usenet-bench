//go:build windows

package nativeadapter

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// Resident memory for the natively launched client and everything it starts.
//
// Tree membership is not rediscovered here: the CPU accountant already places
// the client in a job object, and every process it starts is a member by
// inheritance, so the job's own process list is the tree. Each sample asks
// the job who its live members are and adds up their working sets. The high
// point of that sum is the reported figure, and it is the same quantity the
// container and macOS lanes sample, which is what makes the three hosts
// comparable.
//
// Working set is read with a handle opened per sample and closed again.
// Unlike the CPU counter, nothing has to be held past a member's exit: a
// process that has exited holds no resident memory, so its contribution to a
// later sample is correctly zero.
//
// The hint alongside it is the job's own PeakJobMemoryUsed, which is exact
// and catches every spike, but counts committed memory rather than resident
// memory -- a different quantity, reported as a hint and never as a
// substitute.
const (
	windowsMemoryScope     = "client_process_tree"
	windowsMemoryCollector = "windows-job-working-set-sampled"
	windowsMemoryHint      = "windows-job-peak-memory-used"

	windowsMemorySampleInterval = 250 * time.Millisecond

	jobObjectBasicProcessIDList                     = 3
	jobObjectExtendedLimitInformation               = 9
	processQueryInformation                         = 0x0400
	processVMRead                                   = 0x0010
	errorMoreData                     syscall.Errno = 234
)

var (
	psapi                         = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo      = psapi.NewProc("GetProcessMemoryInfo")
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
)

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectExtendedLimitInfo struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type windowsJobMemory struct {
	job syscall.Handle

	mu      sync.Mutex
	peak    uint64
	samples uint64
	failure string

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

func newMemoryAccountant(cpu cpuAccountant, _ *os.Process) memoryAccountant {
	account, ok := cpu.(*windowsCPUAccount)
	if !ok || account.job == 0 {
		return unavailableMemoryAccount{reason: "the client is not in a job object, so its process tree cannot be enumerated for memory accounting"}
	}
	memory := &windowsJobMemory{
		job:  account.job,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	memory.sample()
	go memory.loop()
	return memory
}

func (memory *windowsJobMemory) loop() {
	defer close(memory.done)
	ticker := time.NewTicker(windowsMemorySampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-memory.stop:
			return
		case <-ticker.C:
			memory.sample()
		}
	}
}

func (memory *windowsJobMemory) sample() {
	total, err := memory.workingSetTotal()
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if err != nil {
		if memory.failure == "" {
			memory.failure = err.Error()
		}
		return
	}
	memory.samples++
	if total > memory.peak {
		memory.peak = total
	}
}

// workingSetTotal adds the working set of every process currently in the job.
// A member that exits between the list and the read contributes nothing,
// which is the right answer rather than an error.
func (memory *windowsJobMemory) workingSetTotal() (uint64, error) {
	pids, err := memory.memberPIDs()
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, pid := range pids {
		handle, openErr := syscall.OpenProcess(processQueryInformation|processVMRead, false, pid)
		if openErr != nil {
			continue
		}
		var counters processMemoryCounters
		counters.CB = uint32(unsafe.Sizeof(counters))
		ok, _, _ := procGetProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.CB))
		_ = syscall.CloseHandle(handle)
		if ok == 0 {
			continue
		}
		total += uint64(counters.WorkingSetSize)
	}
	return total, nil
}

// memberPIDs asks the job for its live members, growing the buffer until the
// whole list fits.
func (memory *windowsJobMemory) memberPIDs() ([]uint32, error) {
	capacity := 64
	for attempt := 0; attempt < 8; attempt++ {
		// A header of two DWORDs followed by `capacity` ULONG_PTR entries.
		const headerSize = 8
		buffer := make([]byte, headerSize+capacity*int(unsafe.Sizeof(uintptr(0))))
		var returned uint32
		ok, _, callErr := procQueryInformationJobObject.Call(
			uintptr(memory.job),
			jobObjectBasicProcessIDList,
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
			uintptr(unsafe.Pointer(&returned)),
		)
		if ok == 0 {
			if errno, isErrno := callErr.(syscall.Errno); isErrno && errno == errorMoreData {
				capacity *= 4
				continue
			}
			return nil, fmt.Errorf("QueryInformationJobObject(process id list): %w", callErr)
		}
		assigned := *(*uint32)(unsafe.Pointer(&buffer[0]))
		inList := *(*uint32)(unsafe.Pointer(&buffer[4]))
		if assigned > inList {
			capacity = int(assigned) * 2
			continue
		}
		pids := make([]uint32, 0, inList)
		for index := 0; index < int(inList); index++ {
			offset := headerSize + index*int(unsafe.Sizeof(uintptr(0)))
			pids = append(pids, uint32(*(*uintptr)(unsafe.Pointer(&buffer[offset]))))
		}
		return pids, nil
	}
	return nil, fmt.Errorf("the job's process list kept growing past every buffer offered for it")
}

func (memory *windowsJobMemory) measurement(*os.ProcessState) (benchmark.CounterMeasurement, benchmark.CounterMeasurement) {
	memory.stopOnce.Do(func() { close(memory.stop) })
	<-memory.done
	memory.mu.Lock()
	peak, samples, failure := memory.peak, memory.samples, memory.failure
	memory.mu.Unlock()

	version := fmt.Sprintf("GetProcessMemoryInfo@%dms", windowsMemorySampleInterval.Milliseconds())
	sampled := benchmark.MeasuredMeasurement(windowsMemoryScope, windowsMemoryCollector, version, peak)
	switch {
	case samples == 0:
		reason := failure
		if reason == "" {
			reason = "no process-tree memory sample was taken"
		}
		sampled = benchmark.UnavailableMeasurement(windowsMemoryScope, windowsMemoryCollector, version, reason)
	case peak == 0:
		sampled = benchmark.UnavailableMeasurement(windowsMemoryScope, windowsMemoryCollector, version,
			"the client process tree was never observed holding a working set")
	}
	return sampled, memory.highWater()
}

func (memory *windowsJobMemory) highWater() benchmark.CounterMeasurement {
	const version = "QueryInformationJobObject"
	var info jobObjectExtendedLimitInfo
	var returned uint32
	ok, _, callErr := procQueryInformationJobObject.Call(
		uintptr(memory.job),
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		uintptr(unsafe.Pointer(&returned)),
	)
	if ok == 0 {
		return benchmark.UnavailableMeasurement(windowsMemoryScope, windowsMemoryHint, version,
			fmt.Sprintf("QueryInformationJobObject(extended limit information): %v", callErr))
	}
	return benchmark.MeasuredMeasurement(windowsMemoryScope, windowsMemoryHint, version, uint64(info.PeakJobMemoryUsed))
}

// close stops sampling. The job handle belongs to the CPU accountant, which
// closes it; this must not.
func (memory *windowsJobMemory) close() {
	memory.stopOnce.Do(func() { close(memory.stop) })
	<-memory.done
}
