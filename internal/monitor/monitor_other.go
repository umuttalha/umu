//go:build !linux

package monitor

import (
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
	return HostStatus{
		Ok: true,
		Checks: []Check{
			{Resource: "memory", Ok: true, Message: "monitoring not available on this platform"},
			{Resource: "disk", Ok: true, Message: "monitoring not available on this platform"},
		},
	}
}

func checkMemory(store *state.Store, threshold float64) Check {
	return Check{Resource: "memory", Ok: true}
}

func checkDisk(threshold float64) Check {
	return Check{Resource: "disk", Ok: true}
}
