package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// insertEvent schedules a future event. All timers in the game flow through
// here; the 1s sweeper claims due rows with SELECT ... FOR UPDATE SKIP LOCKED.
func insertEvent(ctx context.Context, q Querier, worldID int, kind, refID string, runAt time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO scheduled_events(world_id, kind, ref_id, run_at) VALUES($1,$2,$3,$4)`,
		worldID, kind, refID, runAt)
	return err
}

// ClaimDueEvents atomically claims up to limit due events for one sweep pass.
// SKIP LOCKED makes it safe to run multiple sweepers (future horizontal scale).
func (s *Store) ClaimDueEvents(ctx context.Context, limit int, now time.Time) ([]ScheduledEvent, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, world_id, kind, ref_id, run_at, status
		FROM scheduled_events
		WHERE status='pending' AND run_at <= $1
		ORDER BY run_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledEvent
	var ids []int64
	for rows.Next() {
		var e ScheduledEvent
		if err := rows.Scan(&e.ID, &e.WorldID, &e.Kind, &e.RefID, &e.RunAt, &e.Status); err != nil {
			return nil, err
		}
		out = append(out, e)
		ids = append(ids, e.ID)
	}
	rows.Close()
	if len(ids) > 0 {
		if _, err := s.DB.Exec(ctx,
			`UPDATE scheduled_events SET status='claimed' WHERE id = ANY($1)`, ids); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}

// MarkEventDone marks an event resolved.
func (s *Store) MarkEventDone(ctx context.Context, q Querier, eventID int64) error {
	_, err := q.Exec(ctx, `UPDATE scheduled_events SET status='done' WHERE id=$1`, eventID)
	return err
}

// MarkEventFailed marks an event failed (it will be retried by an operator;
// we never silently drop game timers).
func (s *Store) MarkEventFailed(ctx context.Context, eventID int64, reason string) error {
	_, err := s.DB.Exec(ctx,
		`UPDATE scheduled_events SET status='failed' WHERE id=$1`, eventID)
	_ = reason
	return err
}

// NewUUID is a helper for handlers that need ids outside the store.
func NewUUID() string { return uuid.NewString() }
