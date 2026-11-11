//go:build !windows

package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func collectHost() HostInfo {
	host := HostInfo{
		CPUs:         runtime.NumCPU(),
		Architecture: runtime.GOARCH,
	}
	host.Hostname, _ = os.Hostname()
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		host.Kernel = strings.TrimSpace(string(data))
		host.OSVersion = host.Kernel
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				host.OSName = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
			}
		}
	}
	if host.OSName == "" {
		host.OSName = runtime.GOOS
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail uint64
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			value := parseUint(fields[1]) * 1024
			switch fields[0] {
			case "MemTotal:":
				total = value
			case "MemAvailable:":
				avail = value
			}
		}
		if total > 0 {
			host.MemTotal = total
			host.MemFree = avail
			host.MemUsed = total - avail
			host.MemUsedPct = int(host.MemUsed * 100 / total)
			host.MemAvailable = true
		}
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if secs, ok := parseUintOK(fields[0]); ok {
				host.BootTime = time.Now().Add(-time.Duration(secs) * time.Second)
			}
		}
	}
	return host
}

func diskSpace(path string) (total, free uint64, ok bool) {
	dir := existingDir(path)
	if dir == "" {
		return 0, 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, false
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Blocks) * bsize, uint64(st.Bavail) * bsize, true
}

func processRSS() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, false
	}
	pages, ok := parseUintOK(fields[1])
	if !ok {
		return 0, false
	}
	return pages * uint64(os.Getpagesize()), true
}

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
		parent := filepath.Dir(strings.TrimRight(abs, "/"))
		if parent == abs || parent == "" {
			return ""
		}
		abs = parent
	}
}

func parseUint(s string) uint64 {
	v, _ := parseUintOK(s)
	return v
}

func parseUintOK(s string) (uint64, bool) {
	var v uint64
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		v = v*10 + uint64(r-'0')
	}
	return v, true
}
