package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aichallenge_scheduled/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testMCPServer(t *testing.T, calls *atomic.Int32, isError bool) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", Description: "Repeat text", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{IsError: isError, Content: []mcp.Content{&mcp.TextContent{Text: "echo-result:" + args.Text}}}, nil
	})
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	t.Cleanup(endpoint.Close)
	return endpoint
}

func newTestService(t *testing.T, llmURL, mcpURL string, maxIterations int) *app.Service {
	t.Helper()
	temp := 0.0
	s, err := app.NewService(app.Config{
		Database: filepath.Join(t.TempDir(), "agent.db"), Timezone: "UTC", TaskTimeout: "5s",
		LLM:        app.LLMConfig{Provider: "openai-compatible", Model: "mock", BaseURL: llmURL, APIKey: "private-test-key", Temperature: &temp, MaxIterations: maxIterations},
		MCPServers: []app.MCPServerConfig{{Name: "fixture", URL: mcpURL, Headers: map[string]string{"Authorization": "private-mcp-key"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func requestJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func submitTask(t *testing.T, h http.Handler) app.Task {
	t.Helper()
	w := requestJSON(t, h, http.MethodPost, "/api/tasks", `{"prompt":"repeat hello"}`)
	if w.Code != 202 {
		t.Fatalf("POST /api/tasks = %d: %s", w.Code, w.Body.String())
	}
	var task app.Task
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func waitTask(t *testing.T, h http.Handler, id string) app.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := requestJSON(t, h, http.MethodGet, "/api/tasks/"+id, "")
		if w.Code != 200 {
			t.Fatalf("GET task = %d: %s", w.Code, w.Body.String())
		}
		var task app.Task
		if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		if task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled" {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task did not finish")
	return app.Task{}
}

func TestAgentPlansThenExecutesOverRealHTTP(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	var mu sync.Mutex
	var requests []map[string]any
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "wrong path", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer private-test-key" {
			http.Error(w, "wrong auth", 401)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		requests = append(requests, payload)
		step := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch step {
		case 1:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Use echo to repeat hello"}}]}`)
		case 2:
			defs, _ := payload["tools"].([]any)
			name := ""
			for _, raw := range defs {
				fn, _ := raw.(map[string]any)["function"].(map[string]any)
				if strings.HasSuffix(fmt.Sprint(fn["name"]), "_echo") {
					name = fmt.Sprint(fn["name"])
					break
				}
			}
			if name == "" {
				http.Error(w, "echo definition missing", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": name, "arguments": `{"text":"hello"}`}}}}}}})
		case 3:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Echo completed"}}]}`)
		default:
			http.Error(w, "unexpected LLM request", 500)
		}
	}))
	t.Cleanup(llm.Close)
	s := newTestService(t, llm.URL, mcpEndpoint.URL, 4)
	h := s.Handler()
	task := waitTask(t, h, submitTask(t, h).ID)
	if task.Status != "completed" || task.LLMCalls != 3 {
		t.Fatalf("task = status %q, calls %d, error %q", task.Status, task.LLMCalls, task.Error)
	}
	if mcpCalls.Load() != 1 {
		t.Fatalf("MCP calls = %d, want 1", mcpCalls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("LLM HTTP requests = %d, want 3", len(requests))
	}
	if _, ok := requests[0]["tools"]; ok {
		t.Fatal("planning request exposed tools")
	}
	if requests[1]["tools"] == nil {
		t.Fatal("execution request has no tools")
	}
	third, _ := json.Marshal(requests[2]["messages"])
	if !bytes.Contains(third, []byte("echo-result:hello")) || !bytes.Contains(third, []byte(`"role":"tool"`)) {
		t.Fatalf("tool result missing from third LLM request: %s", third)
	}
}

func TestConfigAPIHidesSecretsAndWriteValidation(t *testing.T) {
	var calls atomic.Int32
	mcpEndpoint := testMCPServer(t, &calls, false)
	s := newTestService(t, "http://127.0.0.1:1", mcpEndpoint.URL, 2)
	h := s.Handler()
	w := requestJSON(t, h, "GET", "/api/config", "")
	if w.Code != 200 {
		t.Fatalf("config status %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("private-test-key")) || bytes.Contains(w.Body.Bytes(), []byte("private-mcp-key")) {
		t.Fatalf("config exposed secret: %s", w.Body.String())
	}
	for _, tc := range []struct {
		name, contentType, origin string
		status                    int
	}{
		{"non-json", "text/plain", "", 415},
		{"cross-origin", "application/json", "https://other.invalid", 403},
		{"empty prompt", "application/json", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(`{"prompt":""}`))
			r.Header.Set("Content-Type", tc.contentType)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			if out.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", out.Code, tc.status, out.Body.String())
			}
		})
	}
}

func TestMCPIsErrorContentReachesFollowingLLMRequest(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, true)
	var llmCalls atomic.Int32
	var sawErrorContent atomic.Bool
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch llmCalls.Add(1) {
		case 1:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Call echo"}}]}`)
		case 2:
			defs, _ := payload["tools"].([]any)
			name := ""
			for _, raw := range defs {
				fn, _ := raw.(map[string]any)["function"].(map[string]any)
				if strings.HasSuffix(fmt.Sprint(fn["name"]), "_echo") {
					name = fmt.Sprint(fn["name"])
					break
				}
			}
			if name == "" {
				http.Error(w, "echo missing", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "bad-1", "type": "function", "function": map[string]any{"name": name, "arguments": `{"text":"bad"}`}}}}}}})
		case 3:
			messages, _ := json.Marshal(payload["messages"])
			if bytes.Contains(messages, []byte("echo-result:bad")) && bytes.Contains(messages, []byte("isError")) {
				sawErrorContent.Store(true)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Reported tool error"}}]}`)
		default:
			http.Error(w, "too many LLM calls", 500)
		}
	}))
	t.Cleanup(llm.Close)
	s := newTestService(t, llm.URL, mcpEndpoint.URL, 4)
	task := waitTask(t, s.Handler(), submitTask(t, s.Handler()).ID)
	if task.Status != "completed" {
		t.Fatalf("task = %s: %s", task.Status, task.Error)
	}
	if mcpCalls.Load() != 1 || llmCalls.Load() != 3 {
		t.Fatalf("MCP/LLM calls = %d/%d", mcpCalls.Load(), llmCalls.Load())
	}
	if !sawErrorContent.Load() {
		t.Fatal("MCP IsError marker and content were not both sent to LLM")
	}
}

func TestAgentScheduledPromptRunsFreshAgentAfterDueTime(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	var llmCalls atomic.Int32
	runAt := time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano)
	const futurePrompt = "Use echo to send the future report"
	var scheduledPlanningSawPrompt atomic.Bool
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		step := llmCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch step {
		case 1:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Create a future schedule"}}]}`)
		case 2:
			args, _ := json.Marshal(map[string]any{"name": "future report", "prompt": futurePrompt, "run_at": runAt, "timezone": "Europe/Moscow"})
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "schedule-1", "type": "function", "function": map[string]any{"name": "schedule_agent_task", "arguments": string(args)}}}}}}})
		case 3:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"The report is scheduled"}}]}`)
		case 4:
			messages, _ := json.Marshal(payload["messages"])
			if bytes.Contains(messages, []byte(futurePrompt)) && bytes.Contains(messages, []byte("Europe/Moscow")) && payload["tools"] == nil {
				scheduledPlanningSawPrompt.Store(true)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Call echo now"}}]}`)
		case 5:
			defs, _ := payload["tools"].([]any)
			name := ""
			for _, raw := range defs {
				fn, _ := raw.(map[string]any)["function"].(map[string]any)
				if strings.HasSuffix(fmt.Sprint(fn["name"]), "_echo") {
					name = fmt.Sprint(fn["name"])
					break
				}
			}
			if name == "" {
				http.Error(w, "echo missing", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "echo-1", "type": "function", "function": map[string]any{"name": name, "arguments": `{"text":"future"}`}}}}}}})
		case 6:
			messages, _ := json.Marshal(payload["messages"])
			if !bytes.Contains(messages, []byte("echo-result:future")) {
				http.Error(w, "MCP result missing", 400)
				return
			}
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Future report sent"}}]}`)
		default:
			http.Error(w, "unexpected LLM call", 500)
		}
	}))
	t.Cleanup(llm.Close)
	s := newTestService(t, llm.URL, mcpEndpoint.URL, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	h := s.Handler()
	initial := waitTask(t, h, submitTask(t, h).ID)
	if initial.Status != "completed" {
		t.Fatalf("initial task = %s: %s", initial.Status, initial.Error)
	}
	if mcpCalls.Load() != 0 {
		t.Fatal("MCP called while only scheduling future work")
	}
	w := requestJSON(t, h, "GET", "/api/schedules", "")
	if w.Code != 200 {
		t.Fatalf("list schedules = %d: %s", w.Code, w.Body.String())
	}
	var schedules []app.Schedule
	if err := json.Unmarshal(w.Body.Bytes(), &schedules); err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 || schedules[0].Prompt != futurePrompt || schedules[0].Timezone != "Europe/Moscow" {
		t.Fatalf("saved schedules = %+v", schedules)
	}
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		out := requestJSON(t, h, "GET", "/api/schedules/"+schedules[0].ID+"/runs", "")
		if out.Code != 200 {
			t.Fatalf("list runs = %d: %s", out.Code, out.Body.String())
		}
		var runs []app.Run
		if err := json.Unmarshal(out.Body.Bytes(), &runs); err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 && runs[0].Status == "completed" {
			if runs[0].TaskID == "" || runs[0].TaskID == initial.ID {
				t.Fatalf("scheduled run task_id = %q", runs[0].TaskID)
			}
			fresh := requestJSON(t, h, "GET", "/api/tasks/"+runs[0].TaskID, "")
			var task app.Task
			if err := json.Unmarshal(fresh.Body.Bytes(), &task); err != nil {
				t.Fatal(err)
			}
			if task.Status != "completed" || task.LLMCalls != 3 {
				t.Fatalf("scheduled task = %+v", task)
			}
			if mcpCalls.Load() != 1 || llmCalls.Load() != 6 || !scheduledPlanningSawPrompt.Load() {
				t.Fatalf("MCP/LLM calls = %d/%d; fresh planning = %v", mcpCalls.Load(), llmCalls.Load(), scheduledPlanningSawPrompt.Load())
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("scheduled agent did not finish: LLM=%d MCP=%d", llmCalls.Load(), mcpCalls.Load())
}

func TestAgentStopsAtConfiguredIterationLimit(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	var llmCalls atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		step := llmCalls.Add(1)
		if step == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Call echo"}}]}`)
			return
		}
		defs, _ := payload["tools"].([]any)
		name := ""
		for _, raw := range defs {
			fn, _ := raw.(map[string]any)["function"].(map[string]any)
			if strings.HasSuffix(fmt.Sprint(fn["name"]), "_echo") {
				name = fmt.Sprint(fn["name"])
				break
			}
		}
		if name == "" {
			http.Error(w, "echo missing", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": fmt.Sprintf("call-%d", step), "type": "function", "function": map[string]any{"name": name, "arguments": `{"text":"again"}`}}}}}}})
	}))
	t.Cleanup(llm.Close)
	s := newTestService(t, llm.URL, mcpEndpoint.URL, 2)
	task := waitTask(t, s.Handler(), submitTask(t, s.Handler()).ID)
	if task.Status != "failed" || !strings.Contains(task.Error, "max_iterations") {
		t.Fatalf("task = %s: %s", task.Status, task.Error)
	}
	if llmCalls.Load() != 3 || mcpCalls.Load() != 2 {
		t.Fatalf("LLM/MCP calls = %d/%d", llmCalls.Load(), mcpCalls.Load())
	}
}

func TestCancelStopsBlockedLLMRequest(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	executing := make(chan struct{})
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
		close(executing)
		<-r.Context().Done()
	}))
	t.Cleanup(llm.Close)
	s := newTestService(t, llm.URL, mcpEndpoint.URL, 4)
	h := s.Handler()
	id := submitTask(t, h).ID
	select {
	case <-executing:
	case <-time.After(3 * time.Second):
		t.Fatal("execution request did not start")
	}
	w := requestJSON(t, h, "POST", "/api/tasks/"+id+"/cancel", `{}`)
	if w.Code != 200 {
		t.Fatalf("cancel = %d: %s", w.Code, w.Body.String())
	}
	task := waitTask(t, h, id)
	if task.Status != "cancelled" {
		t.Fatalf("status = %s: %s", task.Status, task.Error)
	}
	if mcpCalls.Load() != 0 {
		t.Fatal("MCP called after cancellation")
	}
}
