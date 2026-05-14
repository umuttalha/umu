package agent

import (
	"encoding/json"
	"testing"
)

func TestExecRequestJSON(t *testing.T) {
	req := execRequest{
		Command:    "echo hello",
		Env:        []string{"FOO=bar"},
		Workdir:    "/app",
		TimeoutSec: 30,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded execRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Command != req.Command {
		t.Errorf("command mismatch: %q != %q", decoded.Command, req.Command)
	}
	if len(decoded.Env) != 1 || decoded.Env[0] != "FOO=bar" {
		t.Errorf("env mismatch: %v", decoded.Env)
	}
	if decoded.Workdir != "/app" {
		t.Errorf("workdir mismatch: %q", decoded.Workdir)
	}
	if decoded.TimeoutSec != 30 {
		t.Errorf("timeout mismatch: %d", decoded.TimeoutSec)
	}
}

func TestExecLineJSON(t *testing.T) {
	tests := []struct {
		line    execLine
		jsonStr string
	}{
		{execLine{Type: "stdout", Data: "hello\n", Seq: 1}, `"type":"stdout"`},
		{execLine{Type: "stderr", Data: "error\n", Seq: 2}, `"type":"stderr"`},
		{execLine{Type: "exit", Code: 0, Seq: 3}, `"type":"exit"`},
		{execLine{Type: "exit", Code: 1, Seq: 4, Data: "failed"}, `"code":1`},
	}

	for _, tt := range tests {
		data, err := json.Marshal(tt.line)
		if err != nil {
			t.Fatalf("marshal %s: %v", tt.line.Type, err)
		}

		var decoded execLine
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if decoded.Type != tt.line.Type {
			t.Errorf("type mismatch: %q != %q", decoded.Type, tt.line.Type)
		}
	}
}

func TestExecLineExitCode(t *testing.T) {
	line := execLine{Type: "exit", Code: 137}
	data, _ := json.Marshal(line)

	var decoded execLine
	json.Unmarshal(data, &decoded)

	if decoded.Code != 137 {
		t.Errorf("exit code: %d != 137", decoded.Code)
	}
}

func TestExecResultStruct(t *testing.T) {
	result := ExecResult{
		Stdout:   "hello world",
		StdErr:   "",
		ExitCode: 0,
	}

	if result.Stdout != "hello world" {
		t.Errorf("stdout mismatch")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code should be 0")
	}
}

func TestAgentPort(t *testing.T) {
	if AgentPort != 9999 {
		t.Errorf("AgentPort = %d, want 9999", AgentPort)
	}
}

func TestMinimalExecRequest(t *testing.T) {
	req := execRequest{
		Command: "ls",
	}

	data, _ := json.Marshal(req)
	var decoded execRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal minimal: %v", err)
	}

	if decoded.Command != "ls" {
		t.Errorf("command should be 'ls', got %q", decoded.Command)
	}
	if decoded.TimeoutSec != 0 {
		t.Errorf("default timeout should be 0, got %d", decoded.TimeoutSec)
	}
	if decoded.Workdir != "" {
		t.Errorf("default workdir should be empty, got %q", decoded.Workdir)
	}
}

func TestExecLineSequence(t *testing.T) {
	// Verify sequence numbers are preserved across serialization
	for seq := 0; seq < 10; seq++ {
		line := execLine{Type: "stdout", Data: "test", Seq: seq}
		data, _ := json.Marshal(line)

		var decoded execLine
		json.Unmarshal(data, &decoded)

		if decoded.Seq != seq {
			t.Errorf("seq %d: decoded as %d", seq, decoded.Seq)
		}
	}
}
