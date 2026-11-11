// Package sysinfo collects runtime, host, storage and cache facts for the
// admin system information page. All collectors are best-effort: a failing
// probe leaves its section empty instead of failing the page.
package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Snapshot is everything the system information page renders.
type Snapshot struct {
	App       AppInfo
	Runtime   RuntimeInfo
	Host      HostInfo
	Storage   []StorageInfo
	DB        DBInfo
	Cache     CacheInfo
	Process   ProcessInfo
	Build     BuildInfo
	Collected time.Time
}

type AppInfo struct {
	Name          string
	Version       string
	Env           string
	BaseURL       string
	Language      string
	Timezone      string
	GoModule      string
	ServerAddr    string
	MaxBodyBytes  int64
	TrustedProxy  int
	MetricsOn     bool
	InstalledAt   time.Time
	UptimeSeconds int64
}

type RuntimeInfo struct {
	GoVersion    string
	GOOS         string
	GOARCH       string
	NumCPU       int
	NumGoroutine int
	GOMAXPROCS   int
	Compiler     string
	GODEBUG      string
	GOFLAGS      string
}

type HostInfo struct {
	Hostname     string
	OSName       string
	OSVersion    string
	Kernel       string
	Architecture string
	BootTime     time.Time
	CPUs         int
	MemTotal     uint64
	MemFree      uint64
	MemUsed      uint64
	MemUsedPct   int
	MemAvailable bool
}

type StorageInfo struct {
	Label     string
	Path      string
	Total     uint64
	Free      uint64
	Used      uint64
	UsedPct   int
	Available bool
}

type DBInfo struct {
	Driver      string
	Version     string
	DSN         string
	OpenConns   int
	IdleConns   int
	InUseConns  int
	MaxOpen     int
	UsedPct     int
	WaitCount   int64
	WaitSeconds float64
	FileSize    uint64
	FileSizeOK  bool
}

type CacheInfo struct {
	Driver   string
	Version  string
	TTL      string
	AdsCache int
	AdsAge   string
	AdsWarm  bool
}

type ProcessInfo struct {
	PID       int
	RSS       uint64
	RSSOK     bool
	HeapAlloc uint64
	HeapSys   uint64
	HeapInuse uint64
	HeapIdle  uint64
	HeapPct   int
	StackIn   uint64
	Sys       uint64
	NumGC     uint32
	LastGC    time.Time
	GCPauseMs float64
}

type BuildInfo struct {
	GoVersion string
	VCS       string
	VCSTime   string
	VCSDirty  bool
	Settings  []BuildSetting
}

type BuildSetting struct {
	Key   string
	Value string
}

// Collect gathers a full snapshot in a single pass.
func Collect(o Options) Snapshot {
	snap := Snapshot{Collected: time.Now()}
	snap.App = collectApp(o)
	snap.Runtime = collectRuntime()
	snap.Host = collectHost()
	snap.Storage = collectStorage(o)
	snap.DB = collectDB(o)
	snap.Cache = collectCache(o)
	snap.Process = collectProcess()
	snap.Build = collectBuild()
	return snap
}

// Options carries the application facts that sysinfo cannot infer itself.
type Options struct {
	AppName      string
	Version      string
	Env          string
	BaseURL      string
	Language     string
	Timezone     string
	ServerAddr   string
	MaxBodyBytes int64
	TrustedProxy int
	MetricsOn    bool
	StartedAt    time.Time
	DBDriver     string
	DBVersion    string
	DBMaxOpen    int
	DBOpenConns  int
	DBIdleConns  int
	DBInUseConns int
	DBWaitCount  int64
	DBWaitSecs   float64
	DBFile       string
	CacheDriver  string
	CacheVersion int64
	CacheTTL     time.Duration
	AdsCacheRows int
	AdsCacheAge  time.Duration
	AdsCacheWarm bool
	UploadPath   string
	DataPath     string
}

func collectApp(o Options) AppInfo {
	app := AppInfo{
		Name:         o.AppName,
		Version:      o.Version,
		Env:          o.Env,
		BaseURL:      o.BaseURL,
		Language:     o.Language,
		Timezone:     o.Timezone,
		ServerAddr:   o.ServerAddr,
		MaxBodyBytes: o.MaxBodyBytes,
		TrustedProxy: o.TrustedProxy,
		MetricsOn:    o.MetricsOn,
		GoModule:     moduleName(),
	}
	if !o.StartedAt.IsZero() {
		app.UptimeSeconds = int64(time.Since(o.StartedAt).Seconds())
	}
	return app
}

func collectRuntime() RuntimeInfo {
	return RuntimeInfo{
		GoVersion:    runtime.Version(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		NumGoroutine: runtime.NumGoroutine(),
		GOMAXPROCS:   runtime.GOMAXPROCS(0),
		Compiler:     runtime.Compiler,
		GODEBUG:      os.Getenv("GODEBUG"),
		GOFLAGS:      os.Getenv("GOFLAGS"),
	}
}

func collectProcess() ProcessInfo {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	p := ProcessInfo{
		PID:       os.Getpid(),
		HeapAlloc: m.HeapAlloc,
		HeapSys:   m.HeapSys,
		HeapInuse: m.HeapInuse,
		HeapIdle:  m.HeapIdle,
		StackIn:   m.StackInuse,
		Sys:       m.Sys,
		NumGC:     m.NumGC,
	}
	if m.LastGC > 0 {
		p.LastGC = time.Unix(0, int64(m.LastGC))
	}
	if m.NumGC > 0 {
		p.GCPauseMs = float64(m.PauseTotalNs) / float64(m.NumGC) / 1e6
	}
	if m.HeapSys > 0 {
		p.HeapPct = int(m.HeapAlloc * 100 / m.HeapSys)
	}
	if rss, ok := processRSS(); ok {
		p.RSS = rss
		p.RSSOK = true
	}
	return p
}

func collectBuild() BuildInfo {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return BuildInfo{}
	}
	out := BuildInfo{GoVersion: info.GoVersion}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			out.VCS = s.Value
		case "vcs.time":
			out.VCSTime = s.Value
		case "vcs.modified":
			out.VCSDirty = s.Value == "true"
		case "-ldflags", "CGO_ENABLED":
			out.Settings = append(out.Settings, BuildSetting{Key: s.Key, Value: s.Value})
		}
	}
	return out
}

func collectDB(o Options) DBInfo {
	db := DBInfo{
		Driver:      o.DBDriver,
		Version:     o.DBVersion,
		DSN:         "",
		OpenConns:   o.DBOpenConns,
		IdleConns:   o.DBIdleConns,
		InUseConns:  o.DBInUseConns,
		MaxOpen:     o.DBMaxOpen,
		WaitCount:   o.DBWaitCount,
		WaitSeconds: o.DBWaitSecs,
	}
	if o.DBMaxOpen > 0 {
		db.UsedPct = o.DBOpenConns * 100 / o.DBMaxOpen
	}
	if o.DBFile != "" {
		if st, err := os.Stat(o.DBFile); err == nil {
			db.FileSize = uint64(st.Size())
			db.FileSizeOK = true
		}
	}
	return db
}

func collectCache(o Options) CacheInfo {
	version := "—"
	if o.CacheVersion > 0 {
		version = time.Unix(0, o.CacheVersion).Format("2006-01-02 15:04:05")
	}
	return CacheInfo{
		Driver:   o.CacheDriver,
		Version:  version,
		TTL:      humanDuration(o.CacheTTL),
		AdsCache: o.AdsCacheRows,
		AdsAge:   humanDuration(o.AdsCacheAge),
		AdsWarm:  o.AdsCacheWarm,
	}
}

func collectStorage(o Options) []StorageInfo {
	var out []StorageInfo
	add := func(label, path string) {
		if path == "" {
			return
		}
		info := diskInfo(label, path)
		if info.Available {
			// Two app paths often share one volume; report each volume once
			// and merge their labels instead of duplicating the usage bar.
			for i := range out {
				if out[i].Available && out[i].Total == info.Total && out[i].Free == info.Free {
					out[i].Label += " / " + label
					if info.Path != "" && out[i].Path != info.Path {
						out[i].Path += " · " + info.Path
					}
					return
				}
			}
		}
		out = append(out, info)
	}
	add("数据目录", o.DataPath)
	add("上传目录", o.UploadPath)
	return out
}

func diskInfo(label, path string) StorageInfo {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	info := StorageInfo{Label: label, Path: abs}
	total, free, ok := diskSpace(abs)
	if !ok {
		return info
	}
	info.Total = total
	info.Free = free
	info.Used = total - free
	if total > 0 {
		info.UsedPct = int(info.Used * 100 / total)
	}
	info.Available = true
	return info
}

// DirsSize walks a directory and sums regular file sizes. Missing paths
// return zero rather than an error so the page still renders.
func DirsSize(path string) (uint64, int) {
	var total uint64
	var files int
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if st, err := d.Info(); err == nil && st.Mode().IsRegular() {
			total += uint64(st.Size())
			files++
		}
		return nil
	})
	return total, files
}

func moduleName() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Path == "" {
		return "goforum"
	}
	return info.Main.Path
}

// HumanBytes renders a byte count with binary units.
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatUint(n, 10) + " B"
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	text := strconv.FormatFloat(value, 'f', 1, 64)
	text = strings.TrimSuffix(text, ".0")
	return text + " " + string("KMGTPE"[exp]) + "iB"
}

// HumanDuration renders a duration using the largest two units.
func HumanDuration(d time.Duration) string { return humanDuration(d) }

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Second {
		return strconv.FormatInt(d.Milliseconds(), 10) + " ms"
	}
	secs := int64(d.Seconds())
	parts := make([]string, 0, 2)
	for _, unit := range []struct {
		size int64
		name string
	}{{86400, "天"}, {3600, "小时"}, {60, "分"}, {1, "秒"}} {
		if secs >= unit.size && len(parts) < 2 {
			parts = append(parts, strconv.FormatInt(secs/unit.size, 10)+unit.name)
			secs %= unit.size
		}
	}
	if len(parts) == 0 {
		return "0 秒"
	}
	return strings.Join(parts, " ")
}

// itoa keeps the platform files free of an extra import.
func itoa(n uint64) string { return strconv.FormatUint(n, 10) }
