package metrics

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

var procFS = "/proc"

type ServiceInfo struct {
	ProjectName string
	ServiceName string
	PID         int
	VCPUs       int
	MemoryMB    int
}

type ProcessMetrics struct {
	ServiceInfo
	CPUPercent float64
	RSSMB      float64
	SwapMB     float64
	Alive      bool
}

type Collector struct {
	prevProcTime map[int]uint64
	prevTotal    uint64
	prevTime     time.Time
	numCPUs      int
	clkTck       uint64
}

func NewCollector() *Collector {
	return &Collector{
		prevProcTime: make(map[int]uint64),
		numCPUs:      countCPUs(),
		clkTck:       getClkTck(),
	}
}

func (c *Collector) Collect(services []ServiceInfo) []ProcessMetrics {
	now := time.Now()
	total := readTotalCPU()
	metrics := make([]ProcessMetrics, 0, len(services))

	for _, svc := range services {
		m := ProcessMetrics{
			ServiceInfo: svc,
			Alive:       false,
		}

		utime, stime, alive := readProcStat(svc.PID)
		if !alive {
			delete(c.prevProcTime, svc.PID)
			metrics = append(metrics, m)
			continue
		}
		m.Alive = true

		procTime := utime + stime

		rss, swap := readProcSmaps(svc.PID)
		m.RSSMB = rss
		m.SwapMB = swap

		if prevProc, ok := c.prevProcTime[svc.PID]; ok && c.prevTotal > 0 {
			deltaProc := float64(procTime - prevProc)
			deltaTotal := float64(total - c.prevTotal)
			if deltaTotal > 0 {
				deltaSeconds := float64(c.numCPUs) * deltaTotal / float64(c.clkTck)
				m.CPUPercent = (deltaProc / float64(c.clkTck)) / deltaSeconds * 100.0
			}
		}

		c.prevProcTime[svc.PID] = procTime
		metrics = append(metrics, m)
	}

	c.prevTotal = total
	c.prevTime = now

	return metrics
}

func readProcStat(pid int) (utime, stime uint64, alive bool) {
	data, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", procFS, pid))
	if err != nil {
		return 0, 0, false
	}

	return parseProcStat(string(data))
}

func parseProcStat(data string) (utime, stime uint64, alive bool) {
	parenOpen := strings.IndexByte(data, '(')
	parenClose := strings.LastIndexByte(data, ')')
	if parenOpen < 0 || parenClose < 0 {
		return 0, 0, false
	}

	fields := strings.Fields(data[parenClose+1:])
	if len(fields) < 13 {
		return 0, 0, false
	}

	u, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	s, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, 0, true
	}

	return u, s, true
}

func readProcSmaps(pid int) (rssMB, swapMB float64) {
	data, err := os.ReadFile(fmt.Sprintf("%s/%d/smaps_rollup", procFS, pid))
	if err != nil {
		return readProcStatus(pid)
	}

	var rssKB, swapKB int64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Rss:") {
			rssKB = parseKB(line)
		} else if strings.HasPrefix(line, "Swap:") {
			swapKB = parseKB(line)
		}
	}

	if rssKB == 0 {
		return readProcStatus(pid)
	}

	return float64(rssKB) / 1024.0, float64(swapKB) / 1024.0
}

func readProcStatus(pid int) (rssMB, swapMB float64) {
	data, err := os.ReadFile(fmt.Sprintf("%s/%d/status", procFS, pid))
	if err != nil {
		return 0, 0
	}

	return parseProcStatus(string(data))
}

func parseProcStatus(data string) (rssMB, swapMB float64) {
	var rssKB, swapKB int64
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			rssKB = parseKB(line)
		} else if strings.HasPrefix(line, "VmSwap:") {
			swapKB = parseKB(line)
		}
	}

	return float64(rssKB) / 1024.0, float64(swapKB) / 1024.0
}

func parseKB(line string) int64 {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	val, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0
	}
	if len(parts) >= 3 && strings.EqualFold(parts[2], "mB") {
		return val * 1024
	}
	return val
}

func readTotalCPU() uint64 {
	data, err := os.ReadFile(procFS + "/stat")
	if err != nil {
		return 0
	}

	return parseTotalCPU(string(data))
}

func parseTotalCPU(data string) uint64 {
	line := strings.Split(data, "\n")[0]
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0
	}

	var total uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		total += v
	}
	return total
}

func countCPUs() int {
	data, err := os.ReadFile(procFS + "/cpuinfo")
	if err != nil {
		return 1
	}
	return strings.Count(string(data), "processor")
}

func getClkTck() uint64 {
	conf, err := os.ReadFile("/usr/include/asm-generic/param.h")
	if err == nil {
		for _, line := range strings.Split(string(conf), "\n") {
			if strings.Contains(line, "CLK_TCK") {
				parts := strings.Fields(line)
				for i, p := range parts {
					if p == "CLK_TCK" && i+2 < len(parts) {
						v, err := strconv.ParseUint(parts[i+2], 10, 64)
						if err == nil {
							return v
						}
					}
				}
			}
		}
	}
	return 100
}