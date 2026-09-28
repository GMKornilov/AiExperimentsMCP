package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Service struct {
	cfg         Config
	db          *sql.DB
	httpClient  *http.Client
	taskTimeout time.Duration
	taskMu      sync.Mutex
	taskCancels map[string]context.CancelFunc
	runCtx      context.Context
	stop        context.CancelFunc
	wg          sync.WaitGroup
	startOnce   sync.Once
	logger      *slog.Logger
	callSeq     atomic.Uint64
}

type ServiceOption func(*Service)

func WithLogger(logger *slog.Logger) ServiceOption {
	return func(s *Service) {
		if logger != nil {
			s.logger = logger
		}
	}
}

func NewService(cfg Config, options ...ServiceOption) (*Service, error) {
	db, err := openDB(cfg.Database)
	if err != nil {
		return nil, err
	}
	timeout, err := time.ParseDuration(cfg.TaskTimeout)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Service{cfg: cfg, db: db, httpClient: &http.Client{}, taskTimeout: timeout, taskCancels: map[string]context.CancelFunc{}, runCtx: context.Background(), logger: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s, nil
}
func (s *Service) Close() error {
	if s.stop != nil {
		s.stop()
	}
	s.taskMu.Lock()
	for _, cancel := range s.taskCancels {
		cancel()
	}
	s.taskMu.Unlock()
	s.wg.Wait()
	return s.db.Close()
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"status": "ok"}) })
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/tools", s.handleTools)
	mux.HandleFunc("GET /api/tasks", s.handleTasks)
	mux.HandleFunc("POST /api/tasks", s.handleTasks)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleTask)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.handleTask)
	mux.HandleFunc("GET /api/schedules", s.handleSchedules)
	mux.HandleFunc("POST /api/schedules", s.handleSchedules)
	mux.HandleFunc("PATCH /api/schedules/{id}", s.handleSchedule)
	mux.HandleFunc("DELETE /api/schedules/{id}", s.handleSchedule)
	mux.HandleFunc("GET /api/schedules/{id}/runs", s.handleRuns)
	mux.HandleFunc("/", s.handleFrontend)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PATCH" || r.Method == "PUT" || r.Method == "DELETE" {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					writeError(w, 403, "origin not allowed")
					return
				}
			}
			mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if r.Method != "DELETE" && mediaType != "application/json" {
				writeError(w, 415, "application/json required")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func (s *Service) redact(input string) string {
	return s.redactLogString(input)
}
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		writeError(w, 400, "unexpected trailing JSON")
		return false
	}
	return true
}
func (s *Service) handleConfig(w http.ResponseWriter, r *http.Request) {
	servers := []map[string]string{}
	for _, c := range s.cfg.MCPServers {
		u, _ := url.Parse(c.URL)
		if u == nil {
			continue
		}
		display := u.Scheme + "://" + u.Host
		if u.Path != "" && u.Path != "/" {
			display += "/…"
		}
		servers = append(servers, map[string]string{"name": c.Name, "url": display})
	}
	writeJSON(w, 200, map[string]any{"model": s.cfg.LLM.Model, "provider": s.cfg.LLM.Provider, "temperature": *s.cfg.LLM.Temperature, "max_iterations": s.cfg.LLM.MaxIterations, "timezone": s.cfg.Timezone, "mcp_servers": servers})
}
func (s *Service) handleTools(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tools, servers, _ := s.discover(withTrace(ctx, "", "discovery"))
	for i := range servers {
		servers[i].Error = s.redact(servers[i].Error)
	}
	writeJSON(w, 200, map[string]any{"servers": servers, "tools": tools})
}
func (s *Service) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		tasks, err := s.tasks(r.Context())
		if err != nil {
			writeError(w, 500, "database error")
			return
		}
		writeJSON(w, 200, tasks)
		return
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" || len(in.Prompt) > 20000 {
		writeError(w, 400, "prompt is required (max 20000 characters)")
		return
	}
	t, err := s.createTask(r.Context(), in.Prompt)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.runTask(s.runCtx, t.ID, false, s.cfg.Timezone) }()
	writeJSON(w, 202, t)
}
func (s *Service) handleTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Method == "POST" {
		s.taskMu.Lock()
		cancel := s.taskCancels[id]
		s.taskMu.Unlock()
		if cancel != nil {
			cancel()
		}
		t, err := s.task(r.Context(), id)
		if errors.Is(err, ErrNotFound) {
			writeError(w, 404, "task not found")
			return
		}
		if err != nil {
			writeError(w, 500, "database error")
			return
		}
		if t.Status == "queued" || t.Status == "planning" || t.Status == "executing" {
			changed, e := s.cancelTask(r.Context(), id)
			if e != nil {
				writeError(w, 500, "database error")
				return
			}
			if changed {
				s.event(r.Context(), id, "info", "Отменено пользователем")
			}
			t, _ = s.task(r.Context(), id)
		}
		writeJSON(w, 200, t)
		return
	}
	t, err := s.task(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "task not found")
		return
	}
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, t)
}
func (s *Service) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		v, err := s.schedules(r.Context())
		if err != nil {
			writeError(w, 500, "database error")
			return
		}
		writeJSON(w, 200, v)
		return
	}
	var in struct {
		Name     string `json:"name"`
		Prompt   string `json:"prompt"`
		Cron     string `json:"cron"`
		Timezone string `json:"timezone"`
		RunAt    string `json:"run_at"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	v := Schedule{Name: in.Name, Prompt: in.Prompt, Cron: in.Cron, Timezone: in.Timezone}
	if in.RunAt != "" {
		t, err := time.Parse(time.RFC3339, in.RunAt)
		if err != nil {
			writeError(w, 400, "run_at must be RFC3339")
			return
		}
		v.RunAt = &t
	}
	if err := s.validateSchedule(r.Context(), &v); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	v, err := s.createSchedule(r.Context(), v)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 201, v)
}
func (s *Service) handleSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.schedule(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if v.Status == "running" {
		writeError(w, 409, "schedule is running")
		return
	}
	if r.Method == "DELETE" {
		if err := s.deleteSchedule(r.Context(), id); err != nil {
			if errors.Is(err, ErrNotFound) {
				writeError(w, 409, "schedule is running")
				return
			}
			writeError(w, 500, "database error")
			return
		}
		w.WriteHeader(204)
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeError(w, 400, "enabled is required")
		return
	}
	var next *time.Time
	if *in.Enabled {
		if v.RunAt != nil {
			if !v.RunAt.After(time.Now()) {
				writeError(w, 409, "run_at is in the past")
				return
			}
			next = v.RunAt
		} else {
			n, e := nextCron(v.Cron, v.Timezone, time.Now())
			if e != nil {
				writeError(w, 400, e.Error())
				return
			}
			next = &n
		}
	}
	v, err = s.setScheduleEnabled(r.Context(), id, *in.Enabled, next)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, 409, "schedule is running")
			return
		}
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, v)
}
func (s *Service) handleRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.schedule(r.Context(), id); errors.Is(err, ErrNotFound) {
		writeError(w, 404, "schedule not found")
		return
	} else if err != nil {
		writeError(w, 500, "database error")
		return
	}
	v, err := s.runs(r.Context(), id)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, v)
}
func (s *Service) handleFrontend(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, 404, "not found")
		return
	}
	path := filepath.Join("frontend", "dist")
	if _, err := os.Stat(path); err != nil {
		writeError(w, 404, "frontend is not built")
		return
	}
	p := filepath.Join(path, filepath.Clean("/"+r.URL.Path))
	if _, err := os.Stat(p); err != nil {
		p = filepath.Join(path, "index.html")
	}
	http.ServeFile(w, r, p)
}
