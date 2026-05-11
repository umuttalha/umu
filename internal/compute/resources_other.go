//go:build !linux

package compute

type ResourceUsage struct {
	CPUUsageSec      float64 `json:"cpu_usage_sec"`
	CPULimit         float64 `json:"cpu_limit"`
	MemoryCurrentMB  float64 `json:"memory_current_mb"`
	MemoryLimitMB    float64 `json:"memory_limit_mb"`
	MemoryPeakMB     float64 `json:"memory_peak_mb"`
	MemorySwapMB     float64 `json:"memory_swap_mb"`
}

func GetResourceUsage(vmName string) (ResourceUsage, bool) {
	return ResourceUsage{}, false
}

func GetDiskUsage(diskPath string) (int64, error) {
	return 0, nil
}

func GetProjectDiskUsage(projectName, serviceName string) string {
	return "0 MB"
}
