package app

import "time"

type Event struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}
type Task struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"`
	Plan      string    `json:"plan"`
	Result    string    `json:"result"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	LLMCalls  int       `json:"llm_calls"`
	Events    []Event   `json:"events"`
}
type Schedule struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prompt    string     `json:"prompt"`
	Cron      string     `json:"cron"`
	Timezone  string     `json:"timezone"`
	RunAt     *time.Time `json:"run_at,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	Enabled   bool       `json:"enabled"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	LastError string     `json:"last_error,omitempty"`
}
type Run struct {
	ID         string     `json:"id"`
	ScheduleID string     `json:"schedule_id"`
	TaskID     string     `json:"task_id"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Result     string     `json:"result"`
	Error      string     `json:"error"`
}
type Tool struct {
	Name        string `json:"name"`
	Server      string `json:"server"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
	apiName     string
}
type ServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
