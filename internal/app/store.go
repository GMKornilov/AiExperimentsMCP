package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func stamp(t time.Time) string      { return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }
func parseStamp(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func nullableStamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return stamp(*t)
}
func openDB(path string) (*sql.DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY,prompt TEXT NOT NULL,status TEXT NOT NULL,plan TEXT NOT NULL DEFAULT '',result TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,llm_calls INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS events (id INTEGER PRIMARY KEY AUTOINCREMENT,task_id TEXT NOT NULL,kind TEXT NOT NULL,message TEXT NOT NULL,created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS schedules (id TEXT PRIMARY KEY,name TEXT NOT NULL,prompt TEXT NOT NULL,cron TEXT NOT NULL DEFAULT '',timezone TEXT NOT NULL,run_at TEXT,next_run_at TEXT,enabled INTEGER NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL,last_error TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY,schedule_id TEXT NOT NULL,task_id TEXT NOT NULL,status TEXT NOT NULL,started_at TEXT NOT NULL,finished_at TEXT,result TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize database: %w", err)
		}
	}
	// An interrupted call may have happened remotely; never retry it automatically.
	now := stamp(time.Now())
	_, err = db.Exec(`UPDATE runs SET status='interrupted',finished_at=?,error='process stopped during agent task' WHERE status='running'`, now)
	if err == nil {
		_, err = db.Exec(`UPDATE schedules SET enabled=0,status='interrupted',next_run_at=NULL,last_error='previous agent task outcome is unknown' WHERE id IN (SELECT schedule_id FROM runs WHERE status='interrupted') AND status='running'`)
	}
	if err == nil {
		_, err = db.Exec(`UPDATE tasks SET status='failed',error='process stopped before task completed',updated_at=? WHERE status IN ('queued','planning','executing')`, now)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func (s *Service) createTask(ctx context.Context, prompt string) (Task, error) {
	now := time.Now().UTC()
	t := Task{ID: newID(), Prompt: prompt, Status: "queued", CreatedAt: now, UpdatedAt: now, Events: []Event{}}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tasks(id,prompt,status,created_at,updated_at) VALUES(?,?,?,?,?)`, t.ID, t.Prompt, t.Status, stamp(now), stamp(now))
	return t, err
}
func (s *Service) task(ctx context.Context, id string) (Task, error) {
	var t Task
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,prompt,status,plan,result,error,created_at,updated_at,llm_calls FROM tasks WHERE id=?`, id).Scan(&t.ID, &t.Prompt, &t.Status, &t.Plan, &t.Result, &t.Error, &created, &updated, &t.LLMCalls)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.CreatedAt = parseStamp(created)
	t.UpdatedAt = parseStamp(updated)
	t.Events = []Event{}
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,message,created_at FROM events WHERE task_id=? ORDER BY id`, id)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		var at string
		if err = rows.Scan(&e.ID, &e.Kind, &e.Message, &at); err != nil {
			return t, err
		}
		e.CreatedAt = parseStamp(at)
		t.Events = append(t.Events, e)
	}
	return t, rows.Err()
}
func (s *Service) tasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for _, id := range ids {
		t, e := s.task(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, nil
}
func (s *Service) updateTask(ctx context.Context, id, status, plan, result, errText string, llmCalls int) error {
	where := "id=? AND status!='cancelled'"
	if status == "cancelled" {
		where = "id=?"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?,plan=?,result=?,error=?,updated_at=?,llm_calls=? WHERE `+where, status, plan, result, errText, stamp(time.Now()), llmCalls, id)
	return err
}
func (s *Service) cancelTask(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status='cancelled',error='cancelled by user',updated_at=? WHERE id=? AND status IN ('queued','planning','executing')`, stamp(time.Now()), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
func (s *Service) event(ctx context.Context, id, kind, message string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO events(task_id,kind,message,created_at) VALUES(?,?,?,?)`, id, kind, message, stamp(time.Now()))
}
func (s *Service) createSchedule(ctx context.Context, v Schedule) (Schedule, error) {
	v.ID = newID()
	v.CreatedAt = time.Now().UTC()
	v.Status = "scheduled"
	v.Enabled = true
	_, err := s.db.ExecContext(ctx, `INSERT INTO schedules(id,name,prompt,cron,timezone,run_at,next_run_at,enabled,status,created_at,last_error) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Name, v.Prompt, v.Cron, v.Timezone, nullableStamp(v.RunAt), nullableStamp(v.NextRunAt), 1, v.Status, stamp(v.CreatedAt), "")
	return v, err
}
func scanSchedule(scanner interface{ Scan(...any) error }) (Schedule, error) {
	var v Schedule
	var created string
	var runAt, next sql.NullString
	var enabled int
	err := scanner.Scan(&v.ID, &v.Name, &v.Prompt, &v.Cron, &v.Timezone, &runAt, &next, &enabled, &v.Status, &created, &v.LastError)
	if err != nil {
		return v, err
	}
	v.CreatedAt = parseStamp(created)
	v.Enabled = enabled != 0
	if runAt.Valid {
		t := parseStamp(runAt.String)
		v.RunAt = &t
	}
	if next.Valid {
		t := parseStamp(next.String)
		v.NextRunAt = &t
	}
	return v, nil
}

const scheduleColumns = `id,name,prompt,cron,timezone,run_at,next_run_at,enabled,status,created_at,last_error`

func (s *Service) schedule(ctx context.Context, id string) (Schedule, error) {
	v, err := scanSchedule(s.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s *Service) schedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		v, e := scanSchedule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Service) setScheduleEnabled(ctx context.Context, id string, enabled bool, next *time.Time) (Schedule, error) {
	status := "paused"
	n := 0
	if enabled {
		status = "scheduled"
		n = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET enabled=?,status=?,next_run_at=?,last_error='' WHERE id=? AND status!='running'`, n, status, nullableStamp(next), id)
	if err != nil {
		return Schedule{}, err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return Schedule{}, ErrNotFound
	}
	return s.schedule(ctx, id)
}
func (s *Service) deleteSchedule(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id=? AND status!='running'`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Service) runs(ctx context.Context, id string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,schedule_id,task_id,status,started_at,finished_at,result,error FROM runs WHERE schedule_id=? ORDER BY started_at DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var started string
		var finished sql.NullString
		if err = rows.Scan(&r.ID, &r.ScheduleID, &r.TaskID, &r.Status, &started, &finished, &r.Result, &r.Error); err != nil {
			return nil, err
		}
		r.StartedAt = parseStamp(started)
		if finished.Valid {
			t := parseStamp(finished.String)
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// claimDue changes the schedule before starting an external call. A process crash is detected at next startup.
func (s *Service) claimDue(ctx context.Context, now time.Time) (Schedule, string, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Schedule{}, "", "", err
	}
	defer tx.Rollback()
	v, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE enabled=1 AND next_run_at<=? AND status='scheduled' ORDER BY next_run_at LIMIT 1`, stamp(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return v, "", "", ErrNotFound
	}
	if err != nil {
		return v, "", "", err
	}
	rid := newID()
	tid := newID()
	if _, err = tx.ExecContext(ctx, `UPDATE schedules SET status='running',next_run_at=NULL WHERE id=?`, v.ID); err != nil {
		return v, "", "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,prompt,status,created_at,updated_at) VALUES(?,?,'queued',?,?)`, tid, v.Prompt, stamp(now), stamp(now)); err != nil {
		return v, "", "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(task_id,kind,message,created_at) VALUES(?,'info',?,?)`, tid, "Запуск расписания «"+v.Name+"»", stamp(now)); err != nil {
		return v, "", "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,schedule_id,task_id,status,started_at) VALUES(?,?,?,'running',?)`, rid, v.ID, tid, stamp(now)); err != nil {
		return v, "", "", err
	}
	return v, rid, tid, tx.Commit()
}
func (s *Service) finishRun(ctx context.Context, v Schedule, rid, result, errText string, uncertain bool) error {
	now := time.Now()
	status := "completed"
	next := (*time.Time)(nil)
	enabled := 0
	schedStatus := "completed"
	if errText != "" {
		status = "failed"
		schedStatus = "failed"
	}
	if uncertain {
		status = "interrupted"
		schedStatus = "interrupted"
		errText = "agent task outcome is unknown"
	} else if v.Cron != "" {
		n, e := nextCron(v.Cron, v.Timezone, now)
		if e == nil {
			next = &n
			enabled = 1
			schedStatus = "scheduled"
		} else {
			errText = e.Error()
			schedStatus = "failed"
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status=?,finished_at=?,result=?,error=? WHERE id=?`, status, stamp(now), result, errText, rid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedules SET status=?,enabled=?,next_run_at=?,last_error=? WHERE id=?`, schedStatus, enabled, nullableStamp(next), errText, v.ID); err != nil {
		return err
	}
	return tx.Commit()
}
