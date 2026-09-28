package app_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aichallenge_scheduled/internal/app"
)

type lockedLogWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedLogWriter) records(t *testing.T) []map[string]any {
	t.Helper()
	w.mu.Lock()
	data := append([]byte(nil), w.b.Bytes()...)
	w.mu.Unlock()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("invalid JSON log %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func loggedService(t *testing.T, llmURL, mcpURL string, headers map[string]string, log *lockedLogWriter, apiKeys ...string) *app.Service {
	t.Helper()
	temp := 0.0
	apiKey := "llm-private-key"
	if len(apiKeys) > 0 {
		apiKey = apiKeys[0]
	}
	s, err := app.NewService(app.Config{
		Database: filepath.Join(t.TempDir(), "logging.db"), Timezone: "UTC", TaskTimeout: "5s",
		LLM: app.LLMConfig{Provider: "openai-compatible", Model: "mock", BaseURL: llmURL,
			APIKey: apiKey, Temperature: &temp, MaxIterations: 4},
		MCPServers: []app.MCPServerConfig{{Name: "fixture", URL: mcpURL, Headers: headers}},
	}, app.WithLogger(slog.New(slog.NewJSONHandler(log, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func callRecords(t *testing.T, log *lockedLogWriter, message string) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, record := range log.records(t) {
		if record["msg"] == message && record["status"] != "started" {
			result = append(result, record)
		}
	}
	return result
}

func assertCallIdentity(t *testing.T, records []map[string]any, taskID string) {
	t.Helper()
	seen := map[string]bool{}
	for _, record := range records {
		if record["task_id"] != taskID || record["stage"] == "" || record["call_id"] == "" {
			t.Fatalf("missing call correlation: %#v", record)
		}
		id := fmt.Sprint(record["call_id"])
		if seen[id] {
			t.Fatalf("duplicate call_id %q", id)
		}
		seen[id] = true
		if duration, ok := record["duration_ms"].(float64); !ok || duration < 0 {
			t.Fatalf("invalid duration_ms: %#v", record)
		}
	}
}

func assertCallStartsBeforeEnd(t *testing.T, log *lockedLogWriter, message string) {
	t.Helper()
	started := map[string]int{}
	ended := map[string]int{}
	for i, record := range log.records(t) {
		if record["msg"] != message {
			continue
		}
		id := fmt.Sprint(record["call_id"])
		if record["status"] == "started" {
			started[id] = i
		} else {
			ended[id] = i
		}
	}
	if len(ended) == 0 || len(started) != len(ended) {
		t.Fatalf("unpaired %s call records: starts=%d ends=%d", message, len(started), len(ended))
	}
	for id, end := range ended {
		if begin, ok := started[id]; !ok || begin >= end {
			t.Fatalf("%s call %s has no earlier start record", message, id)
		}
	}
}

func TestCallLogsCoverLLMAndMCPIsError(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, true)
	var llmCalls atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch llmCalls.Add(1) {
		case 1:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Plan"}}]}`)
		case 2:
			for _, raw := range payload["tools"].([]any) {
				fn := raw.(map[string]any)["function"].(map[string]any)
				if name := fmt.Sprint(fn["name"]); strings.HasSuffix(name, "_echo") {
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": name, "arguments": `{"text":"mcp-private-token"}`}}}}}}})
					return
				}
			}
			http.Error(w, "echo missing", 400)
		case 3:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Reported tool error"}}]}`)
		default:
			http.Error(w, "unexpected call", 500)
		}
	}))
	t.Cleanup(llm.Close)
	log := &lockedLogWriter{}
	s := loggedService(t, llm.URL, mcpEndpoint.URL, map[string]string{"Authorization": "Bearer mcp-private-token"}, log)
	task := waitTask(t, s.Handler(), submitTask(t, s.Handler()).ID)
	if task.Status != "completed" || mcpCalls.Load() != 1 {
		t.Fatalf("task = %s, MCP calls = %d, error = %s", task.Status, mcpCalls.Load(), task.Error)
	}
	llmLogs := callRecords(t, log, "llm_call")
	assertCallStartsBeforeEnd(t, log, "llm_call")
	if len(llmLogs) != 3 {
		t.Fatalf("LLM log count = %d, want 3", len(llmLogs))
	}
	assertCallIdentity(t, llmLogs, task.ID)
	for _, record := range llmLogs {
		if record["status"] != "ok" || record["request"] == "" || record["response"] == "" {
			t.Fatalf("incomplete LLM success log: %#v", record)
		}
	}
	mcpLogs := callRecords(t, log, "mcp_call")
	assertCallStartsBeforeEnd(t, log, "mcp_call")
	assertCallIdentity(t, mcpLogs, task.ID)
	var initialized, listed, toolError bool
	for _, record := range mcpLogs {
		switch record["method"] {
		case "initialize":
			initialized = true
		case "tools/list":
			listed = true
		case "tools/call":
			toolError = record["status"] == "error" && strings.Contains(fmt.Sprint(record["response"]), "echo-result:[REDACTED]") && strings.Contains(fmt.Sprint(record["response"]), "isError")
		}
	}
	if !initialized || !listed || !toolError {
		t.Fatalf("missing MCP initialize/list/IsError call logs: %#v", mcpLogs)
	}
	for _, secret := range []string{"llm-private-key", "mcp-private-token"} {
		if strings.Contains(fmt.Sprint(log.records(t)), secret) {
			t.Fatalf("secret %q leaked into logs", secret)
		}
	}
}

func TestCallLogsCaptureLLMHTTPFailureAndRedactPayload(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"password":"json-secret","nested":"{\"token\":\"token-secret\"}","authorization":"Bearer payload-secret", "credential":"mcp-private-token",`, 429)
	}))
	t.Cleanup(llm.Close)
	log := &lockedLogWriter{}
	s := loggedService(t, llm.URL, mcpEndpoint.URL, map[string]string{"Authorization": "Bearer mcp-private-token"}, log)
	body := `{"prompt":"llm-private-key Bearer payload-secret {\"token\":\"token-secret\"} ` + strings.Repeat("x", 12000) + `"}`
	w := requestJSON(t, s.Handler(), http.MethodPost, "/api/tasks", body)
	if w.Code != 202 {
		t.Fatalf("submit = %d: %s", w.Code, w.Body.String())
	}
	var submitted app.Task
	if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	task := waitTask(t, s.Handler(), submitted.ID)
	if task.Status != "failed" {
		t.Fatalf("task = %s, error = %s", task.Status, task.Error)
	}
	llmLogs := callRecords(t, log, "llm_call")
	assertCallStartsBeforeEnd(t, log, "llm_call")
	if len(llmLogs) != 1 || llmLogs[0]["status"] != "error" || !strings.Contains(fmt.Sprint(llmLogs[0]["error"]), "429") {
		t.Fatalf("missing LLM HTTP error log: %#v", llmLogs)
	}
	assertCallIdentity(t, llmLogs, task.ID)
	if llmLogs[0]["request_truncated"] != true {
		t.Fatalf("large request was not truncated: %#v", llmLogs[0])
	}
	if len(fmt.Sprint(llmLogs[0]["request"])) > 8192 {
		t.Fatalf("request exceeded log bound")
	}
	encoded, err := json.Marshal(log.records(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"llm-private-key", "mcp-private-token", "payload-secret", "json-secret", "token-secret"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("secret %q leaked into logs", secret)
		}
	}
}

func TestCallLogsCaptureMCPTransportFailure(t *testing.T) {
	mcpEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "MCP unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(mcpEndpoint.Close)
	log := &lockedLogWriter{}
	s := loggedService(t, "http://127.0.0.1:1", mcpEndpoint.URL, map[string]string{"Authorization": "Bearer mcp-private-token"}, log)
	task := waitTask(t, s.Handler(), submitTask(t, s.Handler()).ID)
	if task.Status != "failed" {
		t.Fatalf("task = %s, error = %s", task.Status, task.Error)
	}
	mcpLogs := callRecords(t, log, "mcp_call")
	assertCallStartsBeforeEnd(t, log, "mcp_call")
	if len(mcpLogs) == 0 {
		t.Fatal("MCP failure produced no call log")
	}
	assertCallIdentity(t, mcpLogs, task.ID)
	var sawFailure bool
	for _, record := range mcpLogs {
		if record["status"] == "error" && record["error"] != "" {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatalf("MCP transport error missing from logs: %#v", mcpLogs)
	}
	if calls := callRecords(t, log, "llm_call"); len(calls) != 0 {
		t.Fatalf("LLM called after MCP discovery failed: %#v", calls)
	}
}

func TestCallLogsKeepMalformedLLMResponse(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":`)
	}))
	t.Cleanup(llm.Close)
	log := &lockedLogWriter{}
	s := loggedService(t, llm.URL, mcpEndpoint.URL, nil, log)
	task := waitTask(t, s.Handler(), submitTask(t, s.Handler()).ID)
	if task.Status != "failed" {
		t.Fatalf("task = %s, error = %s", task.Status, task.Error)
	}
	llmLogs := callRecords(t, log, "llm_call")
	assertCallStartsBeforeEnd(t, log, "llm_call")
	if len(llmLogs) != 1 || llmLogs[0]["status"] != "error" || !strings.Contains(fmt.Sprint(llmLogs[0]["response"]), "choices") {
		t.Fatalf("malformed HTTP 200 response missing from log: %#v", llmLogs)
	}
}

func TestCallLogsRedactEscapedConfiguredCredential(t *testing.T) {
	secret := `key"part\tail`
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	escapedSecret := string(encoded[1 : len(encoded)-1])
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "malformed: "+escapedSecret+` {`, 429)
	}))
	t.Cleanup(llm.Close)
	log := &lockedLogWriter{}
	s := loggedService(t, llm.URL, mcpEndpoint.URL, nil, log, secret)
	task := waitTask(t, s.Handler(), submitTask(t, s.Handler()).ID)
	if task.Status != "failed" {
		t.Fatalf("task = %s, error = %s", task.Status, task.Error)
	}
	llmLogs := callRecords(t, log, "llm_call")
	if len(llmLogs) != 1 || llmLogs[0]["status"] != "error" {
		t.Fatalf("missing LLM failure: %#v", llmLogs)
	}
	for _, record := range log.records(t) {
		for _, value := range record {
			field := fmt.Sprint(value)
			if strings.Contains(field, secret) || strings.Contains(field, escapedSecret) {
				t.Fatalf("configured escaped credential leaked in log field")
			}
		}
	}
}

func TestCallLogsCaptureCancelledLLMRequest(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	started := make(chan struct{})
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if payload["tools"] == nil {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Plan"}}]}`)
			return
		}
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(llm.Close)
	log := &lockedLogWriter{}
	s := loggedService(t, llm.URL, mcpEndpoint.URL, nil, log)
	h := s.Handler()
	id := submitTask(t, h).ID
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("execution request did not start")
	}
	var startedCallID string
	for _, record := range log.records(t) {
		if record["msg"] == "llm_call" && record["stage"] == "execution" && record["status"] == "started" {
			startedCallID = fmt.Sprint(record["call_id"])
		}
	}
	if startedCallID == "" {
		t.Fatal("blocked execution request has no started log")
	}
	w := requestJSON(t, h, http.MethodPost, "/api/tasks/"+id+"/cancel", `{}`)
	if w.Code != 200 {
		t.Fatalf("cancel = %d: %s", w.Code, w.Body.String())
	}
	if task := waitTask(t, h, id); task.Status != "cancelled" {
		t.Fatalf("task status = %s", task.Status)
	}
	llmLogs := callRecords(t, log, "llm_call")
	assertCallStartsBeforeEnd(t, log, "llm_call")
	if len(llmLogs) != 2 || llmLogs[1]["status"] != "error" || !strings.Contains(strings.ToLower(fmt.Sprint(llmLogs[1]["error"])), "canceled") {
		t.Fatalf("missing cancelled LLM log: %#v", llmLogs)
	}
	assertCallIdentity(t, llmLogs, id)
	if llmLogs[1]["call_id"] != startedCallID {
		t.Fatalf("cancelled request call_id changed: started=%s ended=%v", startedCallID, llmLogs[1]["call_id"])
	}
}
