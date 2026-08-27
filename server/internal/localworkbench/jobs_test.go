package localworkbench

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestJobsDeduplicateClaimAndRecoverExpiredLease(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 27, 1, 2, 3, 0, time.UTC)
	key := "occurrence-1"

	first, created, err := store.EnqueueJob(t.Context(), EnqueueJobInput{
		Kind:        "automation",
		Payload:     json.RawMessage(`{"automation_id":"a-1"}`),
		DedupeKey:   &key,
		AvailableAt: now,
	})
	if err != nil || !created {
		t.Fatalf("first EnqueueJob() = (%#v, %v, %v)", first, created, err)
	}
	second, created, err := store.EnqueueJob(t.Context(), EnqueueJobInput{
		Kind:        "automation",
		Payload:     json.RawMessage(`{"automation_id":"a-1"}`),
		DedupeKey:   &key,
		AvailableAt: now,
	})
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("deduplicated EnqueueJob() = (%#v, %v, %v)", second, created, err)
	}

	claimed, err := store.ClaimJob(t.Context(), "automation", "worker-1", now, time.Minute)
	if err != nil || claimed.ID != first.ID || claimed.AttemptCount != 1 {
		t.Fatalf("first ClaimJob() = (%#v, %v)", claimed, err)
	}
	if _, err := store.ClaimJob(t.Context(), "automation", "worker-2", now.Add(30*time.Second), time.Minute); !errors.Is(err, ErrJobNotClaimable) {
		t.Fatalf("ClaimJob(active lease) error = %v", err)
	}
	reclaimed, err := store.ClaimJob(t.Context(), "automation", "worker-2", now.Add(time.Minute), time.Minute)
	if err != nil || reclaimed.ID != first.ID || reclaimed.AttemptCount != 2 {
		t.Fatalf("ClaimJob(expired lease) = (%#v, %v)", reclaimed, err)
	}
	if err := store.CompleteJob(t.Context(), reclaimed.ID, "worker-2", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("CompleteJob() error = %v", err)
	}
}

func TestConcurrentJobClaimHasSingleWinner(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if _, _, err := store.EnqueueJob(t.Context(), EnqueueJobInput{
		Kind: "run", Payload: json.RawMessage(`{}`), AvailableAt: now,
	}); err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}

	var wait sync.WaitGroup
	winners := make(chan string, 2)
	for _, owner := range []string{"worker-a", "worker-b"} {
		owner := owner
		wait.Add(1)
		go func() {
			defer wait.Done()
			job, err := store.ClaimJob(t.Context(), "run", owner, now, time.Minute)
			if err == nil {
				winners <- job.ID
				return
			}
			if !errors.Is(err, ErrJobNotClaimable) {
				t.Errorf("ClaimJob(%s) error = %v", owner, err)
			}
		}()
	}
	wait.Wait()
	close(winners)
	count := 0
	for range winners {
		count++
	}
	if count != 1 {
		t.Fatalf("claim winners = %d, want 1", count)
	}
}
