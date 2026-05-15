package health

import (
	"fmt"
	"net/http"
	"time"
)

const (
	DefaultTimeout  = 30 * time.Second
	DefaultInterval = 100 * time.Millisecond
)

// HealthPathForRuntime returns the health check endpoint path for a given runtime.
func HealthPathForRuntime(runtime string) string {
	switch runtime {
	case "quickwit":
		return "/api/v1/version"
	case "sqlite":
		return "/health"
	}
	return "/"
}

// Check polls an HTTP endpoint until it returns 200 or times out.
// Uses the root path "/" for backwards compatibility.
func Check(guestIP string, port int) error {
	return CheckWithTimeout(guestIP, port, DefaultTimeout, DefaultInterval)
}

// CheckWithTimeout polls the HTTP endpoint with custom timeout and interval.
func CheckWithTimeout(guestIP string, port int, timeout, interval time.Duration) error {
	return CheckWithPath(guestIP, port, "/", timeout, interval)
}

// CheckWithPath polls an HTTP endpoint at the given path until it returns 200 or times out.
func CheckWithPath(guestIP string, port int, path string, timeout, interval time.Duration) error {
	url := fmt.Sprintf("http://%s:%d%s", guestIP, port, path)
	deadline := time.Now().Add(timeout)

	client := &http.Client{Timeout: 500 * time.Millisecond}

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(interval)
	}

	return fmt.Errorf("health check timed out after %s for %s", timeout, url)
}
