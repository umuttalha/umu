package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

type ExecResult struct {
	Stdout   string
	StdErr   string
	ExitCode int
}

func ExecCommand(guestIP string, command string, env []string, workdir string, timeout time.Duration) (*ExecResult, error) {
	addr := net.JoinHostPort(guestIP, fmt.Sprintf("%d", AgentPort))

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to agent at %s: %w", addr, err)
	}
	defer conn.Close()

	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	timeoutSec := int(timeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 1
	}

	req := execRequest{
		Command:    command,
		Env:        env,
		Workdir:    workdir,
		TimeoutSec: timeoutSec,
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send command: %w", err)
	}

	// Set a read deadline
	if err := conn.SetReadDeadline(time.Now().Add(timeout + 10*time.Second)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	decoder := json.NewDecoder(conn)
	result := &ExecResult{}

	for {
		var line execLine
		if err := decoder.Decode(&line); err != nil {
			if err == io.EOF {
				break
			}
			return result, fmt.Errorf("read response: %w", err)
		}

		switch line.Type {
		case "stdout":
			result.Stdout += line.Data
			fmt.Fprint(os.Stdout, line.Data)
		case "stderr":
			result.StdErr += line.Data
			fmt.Fprint(os.Stderr, line.Data)
		case "exit":
			result.ExitCode = line.Code
			return result, nil
		}
	}

	return result, nil
}
