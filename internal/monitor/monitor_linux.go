//go:build linux

package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/umuttalha/umut/internal/state"
)

const (
	DefaultDir           = "/var/lib/umut"
	MemWarningThreshold  = 0.80
	DiskWarningThreshold = 0.85
)

type Check struct {
	Resource string  `json:"resource"`
	Current  float64 `json:"current"`
	Limit    float64 `json:"limit"`
	UsagePct float64 `json:"usage_percent"`
	Ok       bool    `json:"ok"`
	Message  string  `json:"message,omitempty"`
}

type HostStatus struct {
	Ok       bool     `json:"ok"`
	Checks   []Check  `json:"checks"`
	Warnings []string `json:"warnings,omitempty"`
}

func CheckHost(store *state.Store, memThreshold, diskThreshold float64) HostStatus {
	if memThreshold <= 0 {
		memThreshold = MemWarningThreshold
	}
	if diskThreshold <= 0 {
		diskThreshold = DiskWarningThreshold
	}

	status := HostStatus{Ok: true}

	memCheck := checkMemory(store, memThreshold)
	status.Checks = append(status.Checks, memCheck)
	if !memCheck.Ok && memCheck.Message != "" {
		status.Warnings = append(status.Warnings, memCheck.Message)
		status.Ok = false
	}

	diskCheck := checkDisk(diskThreshold)
	status.Checks = append(status.Checks, diskCheck)
	if !diskCheck.Ok && diskCheck.Message != "" {
		status.Warnings = append(status.Warnings, diskCheck.Message)
		status.Ok = false
	}

	return status
}

func checkMemory(store *state.Store, threshold float64) Check {
	totalMemMB := totalMemoryMB()
	if totalMemMB <= 0 {
		return Check{Resource: "memory", Ok: true, Message: "memory info unavailable"}
	}

	var allocatedMemMB float64
	projects := store.List()
	for _, p := range projects {
		for _, svc := range p.Services {
			if svc.PID > 0 && isPIDAlive(svc.PID) {
				allocatedMemMB += float64(svc.MemoryMB)
			}
		}
	}

	usagePct := allocatedMemMB / totalMemMB
	ok := usagePct < threshold

	c := Check{
		Resource: "memory",
		Current:  allocatedMemMB,
		Limit:    totalMemMB,
		UsagePct: usagePct * 100,
		Ok:       ok,
	}

	if !ok {
		c.Message = fmt.Sprintf(
			"memory allocation %.0f MB / %.0f MB (%.1f%%) exceeds %.0f%% threshold",
			allocatedMemMB, totalMemMB, usagePct*100, threshold*100,
		)
	}

	return c
}

func checkDisk(threshold float64) Check {
	var stat unix.Statfs_t
	if err := unix.Statfs(DefaultDir, &stat); err != nil {
		return Check{Resource: "disk", Ok: true, Message: "disk info unavailable"}
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	freeBytes := stat.Bfree * uint64(stat.Bsize)
	usedBytes := totalBytes - freeBytes

	if totalBytes == 0 {
		return Check{Resource: "disk", Ok: true, Message: "disk info unavailable"}
	}

	totalMB := float64(totalBytes) / 1024 / 1024
	usedMB := float64(usedBytes) / 1024 / 1024

	usagePct := float64(usedBytes) / float64(totalBytes)
	ok := usagePct < threshold

	c := Check{
		Resource: "disk",
		Current:  usedMB,
		Limit:    totalMB,
		UsagePct: usagePct * 100,
		Ok:       ok,
	}

	if !ok {
		c.Message = fmt.Sprintf(
			"disk usage %.0f MB / %.0f MB (%.1f%%) exceeds %.0f%% threshold on %s",
			usedMB, totalMB, usagePct*100, threshold*100, DefaultDir,
		)
	}

	return c
}

func totalMemoryMB() float64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseFloat(fields[1], 64)
				return kb / 1024
			}
		}
	}
	return 0
}

func isPIDAlive(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}
