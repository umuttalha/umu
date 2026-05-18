package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"time"
)

const AgentPort = 9999

type execRequest struct {
	Command    string   `json:"command"`
	Env        []string `json:"env,omitempty"`
	Workdir    string   `json:"workdir,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty"`
}

type execLine struct {
	Type string `json:"type"` // "stdout", "stderr", "exit"
	Data string `json:"data,omitempty"`
	Code int    `json:"code,omitempty"`
	Seq  int    `json:"seq"`
}

func RunGuestAgent(port int) error {
	addr := net.JoinHostPort("0.0.0.0", fmt.Sprintf("%d", port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("agent listen on %s: %w", addr, err)
	}
	defer ln.Close()

	log.Printf("[umu-agent] Listening on %s", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[umu-agent] Accept error: %v", err)
			continue
		}
		go handleAgentConn(conn)
	}
}

func handleAgentConn(conn net.Conn) {
	defer conn.Close()

	var req execRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		log.Printf("[umu-agent] Decode error: %v", err)
		return
	}

	if req.Command == "" {
		log.Printf("[umu-agent] Empty command received")
		return
	}

	log.Printf("[umu-agent] Executing: %s", req.Command)

	timeout := 60 * time.Second
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Command)
	if req.Workdir != "" {
		cmd.Dir = req.Workdir
	}
	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	} else {
		cmd.Env = os.Environ()
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[umu-agent] Stdout pipe error: %v", err)
		return
	}

	var stderrPipe io.ReadCloser
	stderrPipe, err = cmd.StderrPipe()
	if err != nil {
		log.Printf("[umu-agent] Stderr pipe error: %v", err)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[umu-agent] Start error: %v", err)
		enc := json.NewEncoder(conn)
		enc.Encode(execLine{Type: "exit", Code: 1, Data: err.Error()})
		return
	}

	enc := json.NewEncoder(conn)
	seq := 0
	doneCh := make(chan struct{}, 2)
	errCh := make(chan error, 2)

	readBuf := func(r io.Reader, lineType string) {
		defer func() { doneCh <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				encErr := enc.Encode(execLine{Type: lineType, Data: string(buf[:n]), Seq: seq})
				if encErr != nil {
					errCh <- encErr
					return
				}
				seq++
			}
			if readErr != nil {
				if readErr != io.EOF {
					errCh <- readErr
				}
				return
			}
		}
	}

	go readBuf(stdoutPipe, "stdout")
	go readBuf(stderrPipe, "stderr")

	// Wait for both readers or an encode error
	readersDone := 0
	for readersDone < 2 {
		select {
		case <-doneCh:
			readersDone++
		case encErr := <-errCh:
			log.Printf("[umu-agent] Encode error: %v", encErr)
			cancel()
			goto waitCmd
		}
	}

waitCmd:
	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	if ctx.Err() != nil {
		exitCode = 124
	}

	enc.Encode(execLine{Type: "exit", Code: exitCode, Seq: seq})
}
