package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newStoreTestService(t *testing.T, path string) *Service {
	t.Helper()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{db: db}
}

func dueStoreSchedule(t *testing.T, s *Service) Schedule {
	t.Helper()
	now := time.Now().UTC()
	when := now.Add(-time.Minute)
	v, err := s.createSchedule(context.Background(), Schedule{Name: "due", Prompt: "perform future work", Timezone: "UTC", RunAt: &when, NextRunAt: &when})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAtomicDueClaimCreatesOneRunAndTask(t *testing.T) {
	s := newStoreTestService(t, filepath.Join(t.TempDir(), "claim.db"))
	defer s.db.Close()
	v := dueStoreSchedule(t, s)
	const workers = 8
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := s.claimDue(context.Background(), time.Now())
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	claimed := 0
	for err := range results {
		if err == nil {
			claimed++
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if claimed != 1 {
		t.Fatalf("successful claims = %d, want 1", claimed)
	}
	runs, err := s.runs(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].TaskID == "" || runs[0].Status != "running" {
		t.Fatalf("runs = %+v", runs)
	}
	task, err := s.task(context.Background(), runs[0].TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Prompt != v.Prompt {
		t.Fatalf("task prompt = %q, want %q", task.Prompt, v.Prompt)
	}
}

func TestRestartMarksClaimedRunInterruptedWithoutReplaying(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.db")
	first := newStoreTestService(t, path)
	v := dueStoreSchedule(t, first)
	_, runID, taskID, err := first.claimDue(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.db.Close(); err != nil {
		t.Fatal(err)
	}

	second := newStoreTestService(t, path)
	defer second.db.Close()
	runs, err := second.runs(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != runID || runs[0].TaskID != taskID || runs[0].Status != "interrupted" {
		t.Fatalf("runs after restart = %+v", runs)
	}
	got, err := second.schedule(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Status != "interrupted" {
		t.Fatalf("schedule after restart = %+v", got)
	}
	_, _, _, err = second.claimDue(context.Background(), time.Now().Add(time.Hour))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("interrupted call replayed: %v", err)
	}
}
