//go:build windows

package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	psapi             = syscall.NewLazyDLL("psapi.dll")
	ntdll             = syscall.NewLazyDLL("ntdll.dll")
	procGlobalMem     = kernel32.NewProc("GlobalMemoryStatusEx")
	procDiskFree      = kernel32.NewProc("GetDiskFreeSpaceExW")
	procTickCount     = kernel32.NewProc("GetTickCount64")
	procProcessMemory = psapi.NewProc("GetProcessMemoryInfo")
	procRtlGetVersion = ntdll.NewProc("RtlGetVersion")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

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

type osVersionInfoEx struct {
	OSVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformID        uint32
	CSDVersion        [128]uint16
	ServicePackMajor  uint16
	ServicePackMinor  uint16
	SuiteMask         uint16
	ProductType       byte
	Reserved          byte
}

func collectHost() HostInfo {
	host := HostInfo{CPUs: runtimeCPUs()}
	host.Hostname, _ = os.Hostname()
	host.Architecture = runtimeGOARCH()

	var mem memoryStatusEx
	mem.Length = uint32(unsafe.Sizeof(mem))
	if ret, _, _ := procGlobalMem.Call(uintptr(unsafe.Pointer(&mem))); ret != 0 {
		host.MemTotal = mem.TotalPhys
		host.MemFree = mem.AvailPhys
		host.MemUsed = mem.TotalPhys - mem.AvailPhys
		if mem.TotalPhys > 0 {
			host.MemUsedPct = int(host.MemUsed * 100 / mem.TotalPhys)
		}
		host.MemAvailable = true
	}

	version := osVersionInfoEx{OSVersionInfoSize: uint32(unsafe.Sizeof(osVersionInfoEx{}))}
	if ret, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&version))); ret == 0 {
		host.OSName = "Windows"
		host.OSVersion = itoa(uint64(version.MajorVersion)) + "." +
			itoa(uint64(version.MinorVersion)) + "." + itoa(uint64(version.BuildNumber))
		host.Kernel = "NT " + itoa(uint64(version.MajorVersion)) + "." +
			itoa(uint64(version.MinorVersion))
	} else {
		host.OSName = "Windows"
	}
	if ms, _, _ := procTickCount.Call(); ms != 0 {
		host.BootTime = time.Now().Add(-time.Duration(ms) * time.Millisecond)
	}
	return host
}

func runtimeCPUs() int { return runtime.NumCPU() }

func runtimeGOARCH() string { return runtime.GOARCH }

func diskSpace(path string) (total, free uint64, ok bool) {
	dir := existingDir(path)
	if dir == "" {
		return 0, 0, false
	}
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, false
	}
	var freeAvailable, totalBytes, totalFree uint64
	ret, _, _ := procDiskFree.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 || totalBytes == 0 {
		return 0, 0, false
	}
	return totalBytes, freeAvailable, true
}

func processRSS() (uint64, bool) {
	var counters processMemoryCounters
	counters.CB = uint32(unsafe.Sizeof(counters))
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, false
	}
	ret, _, _ := procProcessMemory.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.CB),
	)
	if ret == 0 {
		return 0, false
	}
	return uint64(counters.WorkingSetSize), true
}

// existingDir returns the nearest existing directory at or above path.
func existingDir(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	for {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			if !strings.HasSuffix(abs, string(os.PathSeparator)) {
				abs += string(os.PathSeparator)
			}
			return abs
		}
		parent := filepath.Dir(strings.TrimRight(abs, `\/`))
		if parent == abs || parent == "" {
			return ""
		}
		abs = parent
	}
}
