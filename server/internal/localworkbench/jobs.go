package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var ErrJobNotClaimable = errors.New("local job is not claimable")

type Job struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Payload        json.RawMessage `json:"payload"`
	Status         string          `json:"status"`
	DedupeKey      *string         `json:"dedupe_key"`
	AvailableAt    string          `json:"available_at"`
	LeaseOwner     *string         `json:"lease_owner"`
	LeaseExpiresAt *string         `json:"lease_expires_at"`
	AttemptCount   int             `json:"attempt_count"`
	Error          *string         `json:"error"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

type EnqueueJobInput struct {
	Kind        string
	Payload     json.RawMessage
	DedupeKey   *string
	AvailableAt time.Time
}

func (s *Store) EnqueueJob(ctx context.Context, input EnqueueJobInput) (Job, bool, error) {
	if input.Kind == "" {
		return Job{}, false, errors.New("local job kind is empty")
	}
	if len(input.Payload) == 0 || !json.Valid(input.Payload) {
		return Job{}, false, errors.New("local job payload must be valid JSON")
	}
	if input.AvailableAt.IsZero() {
		input.AvailableAt = time.Now().UTC()
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	job := Job{
		ID:          uuid.NewString(),
		Kind:        input.Kind,
		Payload:     append(json.RawMessage(nil), input.Payload...),
		Status:      "pending",
		DedupeKey:   input.DedupeKey,
		AvailableAt: input.AvailableAt.UTC().Format(time.RFC3339Nano),
		CreatedAt:   stamp,
		UpdatedAt:   stamp,
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO local_jobs (
		id, kind, payload, status, dedupe_key, available_at, created_at, updated_at
	) VALUES (?, ?, ?, 'pending', ?, ?, ?, ?)`,
		job.ID, job.Kind, string(job.Payload), job.DedupeKey, job.AvailableAt,
		job.CreatedAt, job.UpdatedAt,
	)
	if err != nil {
		return Job{}, false, fmt.Errorf("enqueue local job: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Job{}, false, fmt.Errorf("read enqueued local job result: %w", err)
	}
	if inserted == 1 {
		return job, true, nil
	}
	if input.DedupeKey == nil {
		return Job{}, false, errors.New("local job insert was ignored without a dedupe key")
	}
	existing, err := s.jobByDedupeKey(ctx, input.Kind, *input.DedupeKey)
	if err != nil {
		return Job{}, false, err
	}
	return existing, false, nil
}

func (s *Store) ClaimJob(
	ctx context.Context,
	kind string,
	owner string,
	now time.Time,
	lease time.Duration,
) (Job, error) {
	if kind == "" || owner == "" {
		return Job{}, errors.New("local job claim requires kind and owner")
	}
	if lease <= 0 {
		return Job{}, errors.New("local job lease must be positive")
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	expires := now.UTC().Add(lease).Format(time.RFC3339Nano)
	row := s.db.QueryRowContext(ctx, `UPDATE local_jobs
		SET status = 'running', lease_owner = ?, lease_expires_at = ?,
			attempt_count = attempt_count + 1, updated_at = ?, error = NULL
		WHERE id = (
			SELECT id FROM local_jobs
			WHERE kind = ?
			  AND available_at <= ?
			  AND (status = 'pending' OR (status = 'running' AND lease_expires_at <= ?))
			ORDER BY available_at, created_at, id
			LIMIT 1
		)
		RETURNING id, kind, payload, status, dedupe_key, available_at,
			lease_owner, lease_expires_at, attempt_count, error, created_at, updated_at`,
		owner, expires, stamp, kind, stamp, stamp,
	)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotClaimable
	}
	if err != nil {
		return Job{}, fmt.Errorf("claim local job: %w", err)
	}
	return job, nil
}

func (s *Store) CompleteJob(ctx context.Context, id, owner string, now time.Time) error {
	return s.finishJob(ctx, id, owner, "completed", nil, now)
}

func (s *Store) FailJob(ctx context.Context, id, owner, reason string, now time.Time) error {
	if reason == "" {
		return errors.New("local job failure reason is empty")
	}
	return s.finishJob(ctx, id, owner, "failed", &reason, now)
}

func (s *Store) finishJob(
	ctx context.Context,
	id string,
	owner string,
	status string,
	reason *string,
	now time.Time,
) error {
	result, err := s.db.ExecContext(ctx, `UPDATE local_jobs
		SET status = ?, error = ?, lease_owner = NULL, lease_expires_at = NULL,
			updated_at = ?
		WHERE id = ? AND status = 'running' AND lease_owner = ?`,
		status, reason, now.UTC().Format(time.RFC3339Nano), id, owner,
	)
	if err != nil {
		return fmt.Errorf("finish local job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read finished local job result: %w", err)
	}
	if updated != 1 {
		return ErrJobNotClaimable
	}
	return nil
}

func (s *Store) jobByDedupeKey(ctx context.Context, kind, key string) (Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, `SELECT
		id, kind, payload, status, dedupe_key, available_at,
		lease_owner, lease_expires_at, attempt_count, error, created_at, updated_at
		FROM local_jobs WHERE kind = ? AND dedupe_key = ?`, kind, key))
	if err != nil {
		return Job{}, fmt.Errorf("read deduplicated local job: %w", err)
	}
	return job, nil
}

type jobScanner interface {
	Scan(dest ...any) error
}

func scanJob(row jobScanner) (Job, error) {
	var job Job
	var payload string
	var dedupeKey, leaseOwner, leaseExpiresAt, failure sql.NullString
	if err := row.Scan(
		&job.ID, &job.Kind, &payload, &job.Status, &dedupeKey, &job.AvailableAt,
		&leaseOwner, &leaseExpiresAt, &job.AttemptCount, &failure,
		&job.CreatedAt, &job.UpdatedAt,
	); err != nil {
		return Job{}, err
	}
	job.Payload = json.RawMessage(payload)
	job.DedupeKey = nullStringPointer(dedupeKey)
	job.LeaseOwner = nullStringPointer(leaseOwner)
	job.LeaseExpiresAt = nullStringPointer(leaseExpiresAt)
	job.Error = nullStringPointer(failure)
	return job, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
