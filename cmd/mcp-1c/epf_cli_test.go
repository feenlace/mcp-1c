package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Reuse the real-main child entry point; only the native worker is synthetic here.
func TestEPFCLIExistingConfigurationTool(t *testing.T) {
	t.Run("default", func(t *testing.T) { testEPFCLIConfigurationTool(t, false) })
	t.Run("named", func(t *testing.T) { testEPFCLIConfigurationTool(t, true) })
}

func testEPFCLIConfigurationTool(t *testing.T, named bool) {
	var secret string
	root := t.TempDir()
	tokenFile := filepath.Join(root, "pairing-token")

	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStartupRefusalHelperProcess$", "-test.timeout=15s")
	args := []string{"--epf-listen", address, "--epf-token-file", tokenFile, "--epf-web-origin", "http://127.0.0.1:59342", "--cache-dir", root}
	if named {
		for _, i := range []int{1, 3, 5} {
			args[i] = "fixture=" + args[i]
		}
	}
	cmd.Env = append(os.Environ(), refusalHelperEnv+"=1", "MCP_1C_REFUSAL_ARGS="+strings.Join(args, "\n"), "MCP_1C_BASE_URL=", "MCP_1C_USER=", "MCP_1C_PASSWORD=", "MCP_1C_NO_TTY=1")
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var errout bytes.Buffer
	cmd.Stderr = &errout
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		in.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	call := func(path, auth, body string) (int, []byte, error) {
		r, e := http.NewRequestWithContext(ctx, "POST", "http://"+address+path, strings.NewReader(body))
		if e != nil {
			return 0, nil, e
		}
		r.Header.Set("Authorization", "Bearer "+auth)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:59342")
		response, e := http.DefaultClient.Do(r)
		if e != nil {
			return 0, nil, e
		}
		defer response.Body.Close()
		data, e := io.ReadAll(response.Body)
		return response.StatusCode, data, e
	}
	var session string
	for range 200 {
		raw, _ := os.ReadFile(tokenFile)
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) == 2 {
			secret = lines[1]
		}
		status, data, e := call("/pair", secret, `{"protocol":1,"identity":"synthetic-native"}`)
		if e == nil && status == 200 {
			var p struct {
				Session string `json:"session"`
			}
			_ = json.Unmarshal(data, &p)
			session = p.Session
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("EPF listener did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if session == "" {
		t.Fatal("EPF pair unavailable")
	}
	encoder, decoder := json.NewEncoder(in), json.NewDecoder(out)
	send := func(v any) {
		t.Helper()
		if e := encoder.Encode(v); e != nil {
			t.Fatal(e)
		}
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "synthetic-cli-check", "version": "1"}}})
	readResponse := func(want string) json.RawMessage {
		t.Helper()
		for range 16 {
			var frame json.RawMessage
			if e := decoder.Decode(&frame); e != nil {
				t.Fatal(e)
			}
			var header struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(frame, &header)
			if string(header.ID) == want {
				return frame
			}
			if len(header.ID) > 0 {
				t.Fatal("unexpected MCP response id")
			}
		}
		t.Fatal("MCP notification limit reached")
		return nil
	}
	_ = readResponse("1")
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "get_configuration_info", "arguments": map[string]any{}}})
	var task struct{ ID, Capability, Path string }
	for range 200 {
		status, data, e := call("/poll", session, `{"identity":"synthetic-native"}`)
		if e == nil && status == 200 {
			_ = json.Unmarshal(data, &task)
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("tool did not dispatch")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if task.Path != "/configuration" || task.Capability != "community.configuration" {
		t.Fatal("existing configuration tool did not reach the EPF transport")
	}
	status, _, e := call("/result/"+task.ID, session, `{"identity":"synthetic-native","status":200,"body":{"name":"SyntheticConfiguration","version":"1","vendor":"synthetic","platform_version":"unverified","mode":"file"}}`)
	if e != nil || status != 204 {
		t.Fatal("result refused", status)
	}
	result := readResponse("2")
	if !bytes.Contains(result, []byte("SyntheticConfiguration")) || bytes.Contains(result, []byte(secret)) || bytes.Contains(result, []byte(session)) {
		var shape struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(result, &shape)
		t.Fatalf("tool response mismatch: method=%q id=%s has_result=%t has_error=%t pair_leak=%t session_leak=%t", shape.Method, shape.ID, len(shape.Result) > 0, len(shape.Error) > 0, bytes.Contains(result, []byte(secret)), bytes.Contains(result, []byte(session)))
	}
	in.Close()
	if e := cmd.Wait(); e != nil {
		t.Fatal(fmt.Sprintf("CLI exit failed: %v", e))
	}
}
