package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type chatMessage struct {
	Role             string        `json:"role"`
	Content          any           `json:"content"`
	ToolCalls        []llmToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	ReasoningContent any           `json:"reasoning_content,omitempty"`
}
type llmToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type llmChoice struct {
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}
type chatResponse struct {
	Choices []llmChoice `json:"choices"`
}
type llmToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Tools       []llmToolDef  `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
}

func toolDef(name, description string, schema any) llmToolDef {
	d := llmToolDef{Type: "function"}
	d.Function.Name = name
	d.Function.Description = description
	d.Function.Parameters = schema
	return d
}

var objectSchema = map[string]any{"type": "object", "properties": map[string]any{}}

func (s *Service) toolDefs(tools []Tool) []llmToolDef {
	out := []llmToolDef{}
	for _, t := range tools {
		schema := t.InputSchema
		if schema == nil {
			schema = objectSchema
		}
		out = append(out, toolDef(t.apiName, t.Description, schema))
	}
	out = append(out,
		toolDef("schedule_agent_task", "Schedule a full future agent task. Provide a self-contained prompt describing the action to perform at run time, without repeating the scheduling delay. Provide exactly one of run_at (RFC3339 with timezone) or cron (five fields); timezone is an IANA zone for cron.", map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "cron": map[string]any{"type": "string", "description": "Five-field cron expression, exclusive with run_at"}, "timezone": map[string]any{"type": "string", "description": "IANA time zone for cron, defaults to configured zone"}, "run_at": map[string]any{"type": "string", "description": "One-shot RFC3339 timestamp with explicit UTC offset, exclusive with cron"}}, "required": []string{"name", "prompt"}}),
		toolDef("list_schedules", "List saved schedules", objectSchema),
		toolDef("cancel_schedule", "Delete a saved schedule by id", map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"}}))
	return out
}
func (s *Service) chat(ctx context.Context, msg []chatMessage, defs []llmToolDef) (message chatMessage, callErr error) {
	started := time.Now()
	callID := s.nextCallID()
	reqBody := chatRequest{Model: s.cfg.LLM.Model, Messages: msg, Temperature: *s.cfg.LLM.Temperature, Tools: defs}
	if len(defs) > 0 {
		reqBody.ToolChoice = "auto"
	}
	reqLog, reqTruncated := s.logPayload(reqBody)
	var responseLog string
	var responseTruncated bool
	defer func() {
		status, errorText := "ok", ""
		if callErr != nil {
			status, errorText = "error", s.redactLogString(callErr.Error())
		}
		s.logCall(ctx, "llm_call", "call_id", callID, "provider", s.cfg.LLM.Provider, "model", s.cfg.LLM.Model,
			"duration_ms", time.Since(started).Milliseconds(), "status", status, "request", reqLog,
			"request_truncated", reqTruncated, "response", responseLog, "response_truncated", responseTruncated, "error", errorText)
	}()
	b, err := json.Marshal(reqBody)
	if err != nil {
		return chatMessage{}, err
	}
	url := strings.TrimRight(s.cfg.LLM.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return chatMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.LLM.APIKey)
	s.logCall(ctx, "llm_call", "call_id", callID, "provider", s.cfg.LLM.Provider, "model", s.cfg.LLM.Model,
		"status", "started", "request", reqLog, "request_truncated", reqTruncated)
	res, err := s.httpClient.Do(req)
	if err != nil {
		return chatMessage{}, fmt.Errorf("LLM request failed: %w", err)
	}
	defer res.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if json.Valid(body) {
		responseLog, responseTruncated = s.logPayload(json.RawMessage(body))
	} else {
		responseLog, responseTruncated = s.logPayload(string(body))
	}
	if readErr != nil {
		return chatMessage{}, fmt.Errorf("read LLM response: %w", readErr)
	}
	if len(body) > 4<<20 {
		return chatMessage{}, fmt.Errorf("LLM response exceeds 4 MiB")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return chatMessage{}, fmt.Errorf("LLM HTTP status %d: %s", res.StatusCode, s.safeDiagnostic(string(body)))
	}
	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return chatMessage{}, fmt.Errorf("decode LLM response: %w", err)
	}
	if len(out.Choices) == 0 {
		return chatMessage{}, fmt.Errorf("LLM returned no choices")
	}
	choice := out.Choices[0]
	if choice.FinishReason == "length" || choice.FinishReason == "content_filter" {
		return chatMessage{}, fmt.Errorf("LLM stopped with finish_reason %s", choice.FinishReason)
	}
	return choice.Message, nil
}
func toolCatalog(tools []Tool) string {
	b := strings.Builder{}
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema)
		fmt.Fprintf(&b, "- server=%s tool=%s api_name=%s description=%s input_schema=%s\n", t.Server, t.Name, t.apiName, t.Description, string(schema))
	}
	return b.String()
}
func (s *Service) runTask(parent context.Context, id string, scheduled bool, timezone string) {
	ctx, cancel := context.WithTimeout(parent, s.taskTimeout)
	ctx = withTrace(ctx, id, "discovery")
	s.taskMu.Lock()
	s.taskCancels[id] = cancel
	s.taskMu.Unlock()
	defer func() { cancel(); s.taskMu.Lock(); delete(s.taskCancels, id); s.taskMu.Unlock() }()
	t, err := s.task(ctx, id)
	if err != nil || t.Status == "cancelled" {
		return
	}
	plan, result := "", ""
	calls := 0
	set := func(status, errText string) {
		dbctx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = s.updateTask(dbctx, id, status, s.redact(plan), s.redact(result), s.redact(errText), calls)
	}
	event := func(kind, msg string) {
		dbctx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		s.event(dbctx, id, kind, s.redact(msg))
	}
	fail := func(err error) {
		status := "failed"
		if ctx.Err() != nil {
			status = "cancelled"
		}
		set(status, err.Error())
		event("error", err.Error())
	}
	if ctx.Err() != nil {
		fail(ctx.Err())
		return
	}
	set("planning", "")
	event("stage", "MCP discovery и планирование")
	tools, _, err := s.discover(ctx)
	if err != nil {
		fail(err)
		return
	}
	if s.cfg.LLM.Provider == "demo" {
		s.runDemoTask(ctx, id, t.Prompt, tools, &plan, &result, &calls, set, event)
		return
	}
	if timezone == "" {
		timezone = s.cfg.Timezone
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		fail(fmt.Errorf("invalid task timezone: %w", err))
		return
	}
	now := time.Now()
	contextText := fmt.Sprintf("Current UTC time: %s. Current local date/time: %s. Effective timezone: %s. Available MCP tools:\n%s", now.UTC().Format(time.RFC3339), now.In(loc).Format(time.RFC3339), timezone, toolCatalog(tools))
	planningInstruction := "If the user requests future or recurring work, plan a schedule rather than performing the target actions now."
	if scheduled {
		planningInstruction = "This is the due execution of an existing schedule. Perform the saved task now; do not create another schedule for it."
	}
	planning, err := s.chat(withTrace(ctx, id, "planning"), []chatMessage{{Role: "system", Content: "Create a brief actionable plan for the user's task. Do not call tools. State which MCP tools are needed. " + planningInstruction + " Respond with plain text.\n" + contextText}, {Role: "user", Content: t.Prompt}}, nil)
	calls++
	if err != nil {
		fail(err)
		return
	}
	if len(planning.ToolCalls) > 0 {
		fail(fmt.Errorf("planner attempted tool calls"))
		return
	}
	plan = contentString(planning.Content)
	if plan == "" {
		fail(fmt.Errorf("planner returned empty plan"))
		return
	}
	set("executing", "")
	event("llm", "План получен")
	event("stage", "Исполнение плана")
	executionInstruction := "For future or recurring work, call schedule_agent_task with a self-contained future prompt; do not perform the target calendar/message actions now. Resolve relative times from current UTC time and configured timezone. The scheduled prompt must describe the action at run time without repeating the original delay. run_at and cron are mutually exclusive."
	if scheduled {
		executionInstruction = "This is the due execution of an existing schedule. Perform the saved task now using available MCP tools; do not schedule it again."
	}
	messages := []chatMessage{{Role: "system", Content: "Execute the task using tools when needed. " + executionInstruction + " Report tool errors honestly.\n" + contextText}, {Role: "user", Content: "Task: " + t.Prompt + "\nPlan: " + plan}}
	defs := s.toolDefs(tools)
	for i := 0; i < s.cfg.LLM.MaxIterations; i++ {
		assistant, err := s.chat(withTrace(ctx, id, "execution"), messages, defs)
		calls++
		if err != nil {
			fail(err)
			return
		}
		messages = append(messages, assistant)
		set("executing", "")
		event("llm", fmt.Sprintf("Вызов LLM %d", calls))
		if len(assistant.ToolCalls) == 0 {
			result = contentString(assistant.Content)
			if result == "" {
				fail(fmt.Errorf("LLM returned empty result"))
				return
			}
			if ctx.Err() != nil {
				fail(ctx.Err())
				return
			}
			set("completed", "")
			event("result", "Задача завершена")
			return
		}
		for _, call := range assistant.ToolCalls {
			if call.Type != "function" {
				fail(fmt.Errorf("unsupported tool call type"))
				return
			}
			out, err := s.executeTool(withTrace(ctx, id, "execution"), tools, call.Function.Name, json.RawMessage(call.Function.Arguments))
			if err != nil {
				if out != "" {
					out = fmt.Sprintf(`{"error":%q,"tool_result":%s}`, s.redact(err.Error()), out)
				} else {
					out = fmt.Sprintf(`{"error":%q}`, s.redact(err.Error()))
				}
			}
			event("tool", call.Function.Name)
			messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: s.redact(out)})
			if ctx.Err() != nil {
				fail(ctx.Err())
				return
			}
		}
	}
	fail(fmt.Errorf("max_iterations reached"))
}
func contentString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
func (s *Service) executeTool(ctx context.Context, tools []Tool, name string, args json.RawMessage) (string, error) {
	for _, t := range tools {
		if t.apiName == name {
			return s.callMCP(ctx, t.Server, t.Name, args)
		}
	}
	switch name {
	case "schedule_agent_task":
		var in struct {
			Name     string `json:"name"`
			Prompt   string `json:"prompt"`
			Cron     string `json:"cron"`
			Timezone string `json:"timezone"`
			RunAt    string `json:"run_at"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", err
		}
		v := Schedule{Name: in.Name, Prompt: in.Prompt, Cron: in.Cron, Timezone: in.Timezone}
		if in.RunAt != "" {
			t, err := time.Parse(time.RFC3339, in.RunAt)
			if err != nil {
				return "", err
			}
			v.RunAt = &t
		}
		if err := s.validateSchedule(ctx, &v); err != nil {
			return "", err
		}
		v, err := s.createSchedule(ctx, v)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(v)
		return string(b), nil
	case "list_schedules":
		v, err := s.schedules(ctx)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(v)
		return string(b), nil
	case "cancel_schedule":
		var in struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", err
		}
		if err := s.deleteSchedule(ctx, in.ID); err != nil {
			return "", err
		}
		return `{"deleted":true}`, nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}
func (s *Service) runDemoTask(ctx context.Context, id, prompt string, tools []Tool, plan, result *string, calls *int, set func(string, string), event func(string, string)) {
	*plan = "Найти подходящий MCP инструмент, вызвать его и описать результат."
	*calls = 0
	set("executing", "")
	event("llm", "Demo: план построен")
	event("stage", "Demo: исполнение плана")
	if len(tools) == 0 {
		*result = "Demo: задача получена. MCP инструменты не настроены."
		*calls = 0
		set("completed", "")
		event("result", "Demo: завершено")
		return
	}
	chosen := tools[0]
	args := json.RawMessage(`{}`)
	if strings.Contains(strings.ToLower(prompt), "time") || strings.Contains(strings.ToLower(prompt), "время") {
		for _, t := range tools {
			if t.Name == "get_time" {
				chosen = t
				break
			}
		}
	} else {
		for _, t := range tools {
			if t.Name == "echo" {
				chosen = t
				args, _ = json.Marshal(map[string]any{"text": prompt})
				break
			}
		}
	}
	out, err := s.callMCP(withTrace(ctx, id, "execution"), chosen.Server, chosen.Name, args)
	*calls = 0
	event("tool", chosen.apiName)
	if err != nil {
		set("failed", err.Error())
		event("error", err.Error())
		return
	}
	*result = "Demo: " + out
	set("completed", "")
	event("result", "Demo: завершено")
}
