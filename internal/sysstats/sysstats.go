// Package sysstats samples whole-machine CPU + memory usage in numbers
// that match macOS Activity Monitor's display.
//
// macOS-only today. Two pieces:
//
//   - CPU comes from `top -l 2 -n 0 -s 1`. The first sample averages CPU
//     since boot (useless: a freshly-booted machine reports 0% busy until
//     the first idle slice elapses; a long-uptime machine reports the
//     lifetime average), so we always parse the SECOND sample. Cost is
//     ~1s wall, but Sample() runs in a tea goroutine so the UI doesn't
//     stall.
//
//   - RAM comes from `vm_stat` + `sysctl hw.memsize`. We compute Used =
//     (active + wired + compressed) × pageSize, matching Activity
//     Monitor's "Memory Used" definition. The previous implementation
//     parsed `top`'s "PhysMem used" which lumps in cached/inactive
//     pages and inflates the percentage by 10-15 pp vs. what the user
//     sees in Activity Monitor.
package sysstats

import (
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
)

type Stats struct {
	CPUPct        float64 // 0-100, whole-machine (user + sys)
	MemPct        float64 // 0-100, (active + wired + compressed) / total
	MemTotalBytes uint64  // hw.memsize — physical RAM
	MemUsedBytes  uint64  // (active + wired + compressed) × pageSize
	NumCPU        int     // logical core count (runtime.NumCPU)
}

var (
	cpuRE     = regexp.MustCompile(`CPU usage:\s+([\d.]+)%\s+user,\s+([\d.]+)%\s+sys,\s+([\d.]+)%\s+idle`)
	pageSzRE  = regexp.MustCompile(`page size of (\d+) bytes`)
	pageStRE  = regexp.MustCompile(`Pages\s+([^:]+):\s+(\d+)\.`)
	totalMem  atomic.Uint64 // cached hw.memsize — never changes during a run
)

// Sample reads one CPU/RAM snapshot. Returns zero-valued Stats with no
// error on unsupported platforms — callers can render an empty widget
// without special-casing.
func Sample() (Stats, error) {
	st := Stats{NumCPU: runtime.NumCPU()}
	if runtime.GOOS != "darwin" {
		return st, nil
	}

	if out, err := exec.Command("top", "-l", "2", "-n", "0", "-s", "1").Output(); err == nil {
		st.CPUPct = parseCPU(string(out))
	}

	if out, err := exec.Command("vm_stat").Output(); err == nil {
		if total := totalMemBytes(); total > 0 {
			st.MemTotalBytes = total
			st.MemUsedBytes = parseMemUsed(string(out))
			if total > 0 {
				st.MemPct = float64(st.MemUsedBytes) / float64(total) * 100
				if st.MemPct > 100 {
					st.MemPct = 100
				}
			}
		}
	}

	return st, nil
}

// parseCPU pulls the LAST "CPU usage:" line from top output (the second of
// two samples), and derives busy% as 100 - idle%. Idle is the most stable
// signal — using it avoids user+sys+idle rounding errors creeping in.
func parseCPU(s string) float64 {
	all := cpuRE.FindAllStringSubmatch(s, -1)
	if len(all) == 0 {
		return 0
	}
	m := all[len(all)-1]
	idle, _ := strconv.ParseFloat(m[3], 64)
	pct := 100 - idle
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// parseMemUsed scans `vm_stat` output for the per-page counters and returns
// Used = (active + wired down + occupied by compressor) × pageSize. That
// matches Activity Monitor's "Memory Used" definition (App Memory + Wired
// + Compressed). Cached files / inactive / speculative pages are
// reclaimable on demand and so are EXCLUDED, same as Activity Monitor.
func parseMemUsed(s string) uint64 {
	pageSize := uint64(4096)
	if m := pageSzRE.FindStringSubmatch(s); len(m) == 2 {
		if ps, err := strconv.ParseUint(m[1], 10, 64); err == nil && ps > 0 {
			pageSize = ps
		}
	}
	pages := map[string]uint64{}
	for _, m := range pageStRE.FindAllStringSubmatch(s, -1) {
		key := strings.ToLower(strings.TrimSpace(m[1]))
		v, _ := strconv.ParseUint(m[2], 10, 64)
		pages[key] = v
	}
	return (pages["active"] + pages["wired down"] + pages["occupied by compressor"]) * pageSize
}

// totalMemBytes returns hw.memsize, cached after the first successful read
// (the value never changes for a running kernel).
func totalMemBytes() uint64 {
	if v := totalMem.Load(); v != 0 {
		return v
	}
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || v == 0 {
		return 0
	}
	totalMem.Store(v)
	return v
}
