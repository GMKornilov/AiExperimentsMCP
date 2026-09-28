package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func nextCron(expr, zone string, after time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 || strings.HasPrefix(fields[0], "CRON_TZ=") || strings.HasPrefix(fields[0], "TZ=") {
		return time.Time{}, fmt.Errorf("cron must contain exactly five fields; use timezone separately")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, err
	}
	spec, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	next := spec.Next(after.In(loc))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron has no future occurrence")
	}
	return next.UTC(), nil
}
func (s *Service) validateSchedule(_ context.Context, v *Schedule) error {
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" || len(v.Name) > 200 {
		return fmt.Errorf("name is required (max 200 characters)")
	}
	v.Prompt = strings.TrimSpace(v.Prompt)
	if v.Prompt == "" || len(v.Prompt) > 20000 {
		return fmt.Errorf("prompt is required (max 20000 characters)")
	}
	if (v.Cron == "") == (v.RunAt == nil) {
		return fmt.Errorf("exactly one of cron and run_at is required")
	}
	if v.Timezone == "" {
		v.Timezone = s.cfg.Timezone
	}
	if _, err := time.LoadLocation(v.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	if v.RunAt != nil {
		if !v.RunAt.After(time.Now()) {
			return fmt.Errorf("run_at must be in the future")
		}
		t := v.RunAt.UTC()
		v.RunAt = &t
		v.NextRunAt = &t
	} else {
		next, err := nextCron(v.Cron, v.Timezone, time.Now())
		if err != nil {
			return fmt.Errorf("invalid cron: %w", err)
		}
		v.NextRunAt = &next
	}
	return nil
}
func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		s.runCtx, s.stop = context.WithCancel(ctx)
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.scheduler(s.runCtx) }()
	})
}
func (s *Service) scheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				v, rid, tid, err := s.claimDue(claimCtx, time.Now())
				cancel()
				if errors.Is(err, ErrNotFound) {
					break
				}
				if err != nil {
					break
				}
				s.wg.Add(1)
				go func() { defer s.wg.Done(); s.executeSchedule(ctx, v, rid, tid) }()
			}
		}
	}
}
func (s *Service) executeSchedule(parent context.Context, v Schedule, rid, tid string) {
	s.runTask(parent, tid, true, v.Timezone)
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	task, err := s.task(finishCtx, tid)
	if err != nil {
		_ = s.finishRun(finishCtx, v, rid, "", err.Error(), true)
		return
	}
	uncertain := parent.Err() != nil || task.Status == "cancelled" || (task.Status != "completed" && task.Status != "failed")
	if err := s.finishRun(finishCtx, v, rid, task.Result, task.Error, uncertain); err != nil {
		log.Printf("finish schedule run %s: %v", rid, err)
	}
}
