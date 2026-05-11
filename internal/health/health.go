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

// Check polls an HTTP endpoint until it returns 200 or times out.
func Check(guestIP string, port int) error {
	return CheckWithTimeout(guestIP, port, DefaultTimeout, DefaultInterval)
}

// CheckWithTimeout polls the HTTP endpoint with custom timeout and interval.
func CheckWithTimeout(guestIP string, port int, timeout, interval time.Duration) error {
	url := fmt.Sprintf("http://%s:%d/", guestIP, port)
	deadline := time.Now().Add(timeout)

	client := &http.Client{Timeout: 2 * time.Second}

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
