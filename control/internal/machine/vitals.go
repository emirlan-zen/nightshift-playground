// Package machine reads and caches privilege-free host health signals.
package machine

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Vitals struct {
	Generated int64 `json:"generated"`
	UptimeSec int64 `json:"uptimeSec"`

	Load1    float64 `json:"load1"`
	Load5    float64 `json:"load5"`
	Load15   float64 `json:"load15"`
	CPUCount int     `json:"cpuCount"`

	MemTotalBytes uint64 `json:"memTotalBytes"`
	MemUsedBytes  uint64 `json:"memUsedBytes"`

	SwapTotalBytes uint64 `json:"swapTotalBytes"`
	SwapUsedBytes  uint64 `json:"swapUsedBytes"`

	DiskTotalBytes uint64 `json:"diskTotalBytes"`
	DiskUsedBytes  uint64 `json:"diskUsedBytes"`
	OK             bool   `json:"ok"`
}

// Read gathers each source independently so a missing kernel counter degrades
// the response without hiding the remaining signals.
func Read() Vitals {
	vitals := Vitals{Generated: time.Now().Unix(), CPUCount: runtime.NumCPU(), OK: true}
	if load1, load5, load15, err := readLoadAverage(); err == nil {
		vitals.Load1, vitals.Load5, vitals.Load15 = load1, load5, load15
	} else {
		vitals.OK = false
	}
	if uptime, err := readUptime(); err == nil {
		vitals.UptimeSec = uptime
	} else {
		vitals.OK = false
	}
	if memory, err := readMemory(); err == nil {
		vitals.MemTotalBytes = memory.total
		vitals.MemUsedBytes = memory.used
		vitals.SwapTotalBytes = memory.swapTotal
		vitals.SwapUsedBytes = memory.swapUsed
	} else {
		vitals.OK = false
	}
	if total, used, err := readRootDisk(); err == nil {
		vitals.DiskTotalBytes, vitals.DiskUsedBytes = total, used
	} else {
		// Degrade OK like every other source above — without this a statfs
		// failure left OK=true with 0/0 disk, so the UI rendered "healthy, 0
		// bytes disk" (contradicting this function's own contract).
		vitals.OK = false
	}
	return vitals
}

func readLoadAverage() (float64, float64, float64, error) {
	body, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(body))
	if len(fields) < 3 {
		return 0, 0, 0, os.ErrInvalid
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, 0, err
	}
	load5, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, 0, 0, err
	}
	load15, err := strconv.ParseFloat(fields[2], 64)
	return load1, load5, load15, err
}

func readUptime() (int64, error) {
	body, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return 0, os.ErrInvalid
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	return int64(seconds), err
}

type memory struct {
	total, used, swapTotal, swapUsed uint64
}

func readMemory() (memory, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return memory{}, err
	}
	defer file.Close()
	values := map[string]uint64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = kilobytes * 1024
		}
	}
	if err := scanner.Err(); err != nil {
		return memory{}, err
	}
	result := memory{total: values["MemTotal"], swapTotal: values["SwapTotal"]}
	if result.total == 0 {
		return memory{}, os.ErrInvalid
	}
	if available := values["MemAvailable"]; available <= result.total {
		result.used = result.total - available
	}
	if free := values["SwapFree"]; free <= result.swapTotal {
		result.swapUsed = result.swapTotal - free
	}
	return result, nil
}

// readRootDisk is a package var so tests can stub a statfs failure (the syscall
// can't be made to fail on demand otherwise).
var readRootDisk = func() (uint64, uint64, error) {
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs("/", &filesystem); err != nil {
		return 0, 0, err
	}
	blockSize := uint64(filesystem.Bsize)
	total := filesystem.Blocks * blockSize
	used := (filesystem.Blocks - filesystem.Bavail) * blockSize
	return total, used, nil
}
