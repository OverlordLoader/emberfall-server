// Package scheduler runs the event-driven tick: a 1-second sweeper that
// claims due scheduled_events rows with SELECT ... FOR UPDATE SKIP LOCKED
// and resolves each inside a transaction via sim/. There is no fixed global
// tick; lazy resources + scheduled events carry all game timers.
package scheduler

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// Sweeper claims and resolves due events in a loop until ctx is cancelled.
type Sweeper struct {
	store *store.Store
}

// New builds a sweeper.
func New(s *store.Store) *Sweeper { return &Sweeper{store: s} }

// Run loops forever (until ctx done): claim due events, resolve each in its
// own transaction, publish deltas. A Redis lock per world serializes multiple
// sweeper instances; SKIP LOCKED makes concurrent sweepers safe anyway.
func (sw *Sweeper) Run(ctx context.Context) {
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	// Nightly-ish maintenance: run every hour, cheap no-ops when nothing to do.
	maint := time.NewTicker(1 * time.Hour)
	defer maint.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			sw.sweepOnce(ctx, now)
		case <-maint.C:
			sw.maintenance(ctx)
		}
	}
}

func (sw *Sweeper) sweepOnce(ctx context.Context, now time.Time) {
	events, err := sw.store.ClaimDueEvents(ctx, 200, now)
	if err != nil {
		log.Printf("sweeper: claim: %v", err)
		return
	}
	for _, e := range events {
		if err := sw.resolve(ctx, e, now); err != nil {
			log.Printf("sweeper: event %d (%s %s): %v", e.ID, e.Kind, e.RefID, err)
			_ = sw.store.MarkEventFailed(ctx, e.ID, err.Error())
			continue
		}
		if err := sw.store.TxFunc(ctx, func(ctx context.Context, tx store.Querier) error {
			return sw.store.MarkEventDone(ctx, tx, e.ID)
		}); err != nil {
			log.Printf("sweeper: mark done %d: %v", e.ID, err)
		}
	}
}

// resolve dispatches one claimed event. Each case runs in its own transaction
// so a poison event can't wedge the queue.
func (sw *Sweeper) resolve(ctx context.Context, e store.ScheduledEvent, now time.Time) error {
	return sw.store.TxFunc(ctx, func(ctx context.Context, tx store.Querier) error {
		var err error
		switch e.Kind {
		case "build_done":
			var state string
			_ = tx.QueryRow(ctx, `SELECT state FROM build_queue WHERE id=$1`, e.RefID).Scan(&state)
			if state == "queued" {
				err = sw.store.CompleteBuild(ctx, tx, e.RefID, now)
			}
		case "train_done":
			var state string
			_ = tx.QueryRow(ctx, `SELECT state FROM train_queue WHERE id=$1`, e.RefID).Scan(&state)
			if state == "queued" {
				err = sw.store.CompleteTrain(ctx, tx, e.RefID, now)
			}
		case "march_arrive":
			err = sw.store.ResolveMarchArrival(ctx, tx, e.RefID, now)
		case "march_return":
			err = sw.store.CompleteMarchReturn(ctx, tx, e.RefID, now)
		case "node_respawn":
			var payload struct {
				X int `json:"x"`
				Y int `json:"y"`
			}
			var raw []byte
			if qerr := tx.QueryRow(ctx, `SELECT payload FROM scheduled_events WHERE id=$1`, e.ID).Scan(&raw); qerr == nil {
				_ = json.Unmarshal(raw, &payload)
				err = sw.store.RespawnNode(ctx, tx, e.WorldID, payload.X, payload.Y, now)
			}
		case "shield_expire":
			// Phase 1 stub: shields don't exist yet; just mark done.
		default:
			log.Printf("sweeper: unknown event kind %q", e.Kind)
		}
		if err != nil {
			return err
		}
		// Publish a delta so subscribed WS clients update.
		_ = sw.store.PublishWorld(ctx, e.WorldID, map[string]any{
			"type": "event_resolved", "kind": e.Kind, "ref_id": e.RefID,
		})
		return nil
	})
}

func (sw *Sweeper) maintenance(ctx context.Context) {
	if n, err := sw.store.PruneChat(ctx); err == nil && n > 0 {
		log.Printf("sweeper: pruned %d chat messages", n)
	}
	if n, err := sw.store.PruneIdempotency(ctx); err == nil && n > 0 {
		log.Printf("sweeper: pruned %d idempotency rows", n)
	}
}
