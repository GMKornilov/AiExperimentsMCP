package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"aichallenge_scheduled/internal/app"
)

func scheduleService(t *testing.T, path, llmURL, mcpURL string) *app.Service {
	t.Helper()
	temp := 0.0
	s, err := app.NewService(app.Config{Database: path, Timezone: "UTC", TaskTimeout: "5s", LLM: app.LLMConfig{Provider: "openai-compatible", Model: "mock", BaseURL: llmURL, APIKey: "unused", Temperature: &temp, MaxIterations: 2}, MCPServers: []app.MCPServerConfig{{Name: "fixture", URL: mcpURL}}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOneShotScheduleRunsFreshTaskAfterRestart(t *testing.T) {
	var mcpCalls atomic.Int32
	mcpEndpoint := testMCPServer(t, &mcpCalls, false)
	var llmCalls atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch llmCalls.Add(1) {
		case 1:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Answer the prompt"}}]}`)
		case 2:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Done after restart"}}]}`)
		default:
			http.Error(w, "unexpected LLM request", 500)
		}
	}))
	t.Cleanup(llm.Close)
	db := filepath.Join(t.TempDir(), "durable.db")
	first := scheduleService(t, db, llm.URL, mcpEndpoint.URL)
	runAt := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	w := requestJSON(t, first.Handler(), "POST", "/api/schedules", fmt.Sprintf(`{"name":"once","prompt":"future work","run_at":%q}`, runAt))
	if w.Code != 201 {
		t.Fatalf("create schedule = %d: %s", w.Code, w.Body.String())
	}
	var created app.Schedule
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("schedule ID is empty")
	}
	if llmCalls.Load() != 0 || mcpCalls.Load() != 0 {
		t.Fatal("future work ran before due time")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// run_at passes while the service is stopped. The new process must claim it once.
	time.Sleep(2200 * time.Millisecond)
	second := scheduleService(t, db, llm.URL, mcpEndpoint.URL)
	t.Cleanup(func() { _ = second.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := requestJSON(t, second.Handler(), "GET", "/api/schedules/"+created.ID+"/runs", "")
		if out.Code != 200 {
			t.Fatalf("list runs = %d: %s", out.Code, out.Body.String())
		}
		var runs []app.Run
		if err := json.Unmarshal(out.Body.Bytes(), &runs); err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 && runs[0].Status == "completed" {
			if len(runs) != 1 || runs[0].TaskID == "" {
				t.Fatalf("runs = %+v", runs)
			}
			if llmCalls.Load() != 2 || mcpCalls.Load() != 0 {
				t.Fatalf("LLM/MCP calls = %d/%d", llmCalls.Load(), mcpCalls.Load())
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("overdue one-shot did not complete after restart")
}

func TestScheduleRejectsInvalidTimezoneCronAndPrompt(t *testing.T) {
	var calls atomic.Int32
	mcpEndpoint := testMCPServer(t, &calls, false)
	s := scheduleService(t, filepath.Join(t.TempDir(), "schedule.db"), "http://127.0.0.1:1", mcpEndpoint.URL)
	t.Cleanup(func() { _ = s.Close() })
	for _, tc := range []struct{ name, body string }{
		{"timezone", `{"name":"bad","prompt":"do work","cron":"* * * * *","timezone":"Mars/Olympus"}`},
		{"cron", `{"name":"bad","prompt":"do work","cron":"not cron","timezone":"UTC"}`},
		{"both", `{"name":"bad","prompt":"do work","cron":"* * * * *","run_at":"2030-01-01T00:00:00Z"}`},
		{"missing prompt", `{"name":"bad","run_at":"2030-01-01T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := requestJSON(t, s.Handler(), http.MethodPost, "/api/schedules", tc.body)
			if w.Code != 400 {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCronScheduleAndTimezonePersistAcrossRestart(t *testing.T) {
	var calls atomic.Int32
	mcpEndpoint := testMCPServer(t, &calls, false)
	path := filepath.Join(t.TempDir(), "cron.db")
	first := scheduleService(t, path, "http://127.0.0.1:1", mcpEndpoint.URL)
	w := requestJSON(t, first.Handler(), "POST", "/api/schedules", `{"name":"weekday","prompt":"prepare report","cron":"30 18 * * 1-5","timezone":"Europe/Moscow"}`)
	if w.Code != 201 {
		t.Fatalf("create cron = %d: %s", w.Code, w.Body.String())
	}
	var before app.Schedule
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if before.NextRunAt == nil || !before.NextRunAt.After(time.Now()) {
		t.Fatalf("invalid next run: %+v", before)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := scheduleService(t, path, "http://127.0.0.1:1", mcpEndpoint.URL)
	defer second.Close()
	w = requestJSON(t, second.Handler(), "GET", "/api/schedules", "")
	if w.Code != 200 {
		t.Fatalf("list cron = %d: %s", w.Code, w.Body.String())
	}
	var schedules []app.Schedule
	if err := json.Unmarshal(w.Body.Bytes(), &schedules); err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 || schedules[0].Prompt != "prepare report" || schedules[0].Timezone != "Europe/Moscow" || schedules[0].Cron != "30 18 * * 1-5" || schedules[0].NextRunAt == nil || !schedules[0].NextRunAt.Equal(*before.NextRunAt) {
		t.Fatalf("cron changed after restart: %+v", schedules)
	}
}
