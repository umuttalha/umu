//go:build linux

package compute

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ResourceUsage struct {
	CPUUsageSec      float64 `json:"cpu_usage_sec"`
	CPULimit         float64 `json:"cpu_limit"`
	MemoryCurrentMB  float64 `json:"memory_current_mb"`
	MemoryLimitMB    float64 `json:"memory_limit_mb"`
	MemoryPeakMB     float64 `json:"memory_peak_mb"`
	MemorySwapMB     float64 `json:"memory_swap_mb"`
}

func GetResourceUsage(vmName string) (ResourceUsage, bool) {
	cgDir := filepath.Join(CgroupBaseDir, vmName)
	if _, err := os.Stat(cgDir); os.IsNotExist(err) {
		return ResourceUsage{}, false
	}

	var ru ResourceUsage

	// CPU usage (cumulative microseconds → seconds)
	if data, err := os.ReadFile(filepath.Join(cgDir, "cpu.stat")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "usage_usec ") {
				f, _ := strconv.ParseFloat(strings.TrimPrefix(line, "usage_usec "), 64)
				ru.CPUUsageSec = f / 1_000_000
				break
			}
		}
	}

	// CPU limit (first number in cpu.max)
	if data, err := os.ReadFile(filepath.Join(cgDir, "cpu.max")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 && fields[0] != "max" {
			limit, _ := strconv.ParseFloat(fields[0], 64)
			ru.CPULimit = limit / 100_000 // convert period-based to CPUs
		}
	}

	// Memory current
	if data, err := os.ReadFile(filepath.Join(cgDir, "memory.current")); err == nil {
		bytes, _ := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		ru.MemoryCurrentMB = bytes / 1024 / 1024
	}

	// Memory limit
	if data, err := os.ReadFile(filepath.Join(cgDir, "memory.max")); err == nil {
		text := strings.TrimSpace(string(data))
		if text != "max" {
			bytes, _ := strconv.ParseFloat(text, 64)
			ru.MemoryLimitMB = bytes / 1024 / 1024
		}
	}

	// Memory peak
	if data, err := os.ReadFile(filepath.Join(cgDir, "memory.peak")); err == nil {
		bytes, _ := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		ru.MemoryPeakMB = bytes / 1024 / 1024
	}

	// Memory swap
	if data, err := os.ReadFile(filepath.Join(cgDir, "memory.swap.current")); err == nil {
		bytes, _ := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		ru.MemorySwapMB = bytes / 1024 / 1024
	}

	return ru, true
}

func GetDiskUsage(diskPath string) (int64, error) {
	info, err := os.Stat(diskPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func GetProjectDiskUsage(projectName, serviceName string) string {
	diskPath := filepath.Join("/var/lib/umu/images", fmt.Sprintf("%s-%s.ext4", projectName, serviceName))
	info, err := os.Stat(diskPath)
	if err != nil {
		return "0 MB"
	}
	mb := float64(info.Size()) / 1024 / 1024
	return fmt.Sprintf("%.0f MB", mb)
}
