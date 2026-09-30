// Package anticheat holds server-side legality checks and anomaly flagging.
// Every game action is validated here (or via the store's transactional
// checks); anomalies are flagged for human review. Phase 1 never auto-bans.
package anticheat

import (
	"context"
	"fmt"
	"regexp"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// Legality errors are returned to the client as 400s; anomalies are flagged
// and the action still fails closed.

var (
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9 _.'-]{3,40}$`)
	tagRe    = regexp.MustCompile(`^[A-Za-z0[]]{2,5}$`)
	coordMax = 100000
)

// CheckTroopCounts rejects impossible troop payloads before they reach the DB.
func CheckTroopCounts(t sim.TroopCounts) error {
	for _, n := range []int64{t.Infantry, t.Cavalry, t.Archers, t.Siege} {
		if n < 0 || n > 500000 {
			return fmt.Errorf("troop count out of bounds")
		}
	}
	if t.Total() <= 0 {
		return fmt.Errorf("march needs at least one troop")
	}
	return nil
}

// CheckCoords rejects out-of-world coordinates.
func CheckCoords(x, y, worldSize int) error {
	if x < 0 || y < 0 || x >= worldSize || y >= worldSize {
		return fmt.Errorf("coordinates out of world bounds")
	}
	return nil
}

// CheckName validates player-facing names (cities, alliances, display names).
func CheckName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name")
	}
	return nil
}

// CheckTag validates alliance tags.
func CheckTag(tag string) error {
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("invalid tag")
	}
	return nil
}

// FlagAnomaly records an impossible-state observation for human review.
// It never blocks the caller; the action already failed closed upstream.
func FlagAnomaly(ctx context.Context, s *store.Store, playerID string, worldID *int, reason string, detail map[string]any) {
	_ = s.FlagCheat(ctx, s.DB, playerID, worldID, reason, detail)
}

// ObserveResourceDelta flags resource gains with no source event. Callers pass
// the expected anchor math; a mismatch beyond tolerance is suspicious.
// (Phase 1: wired into the build/train spend paths via reanchoring; kept as a
// helper for future background jobs.)
func ObserveResourceDelta(before, after sim.ResourceBundle, spent sim.Cost, tolerance int64) bool {
	expect := sim.Spend(before, spent)
	for _, d := range []int64{
		after.Wood - expect.Wood,
		after.Stone - expect.Stone,
		after.Food - expect.Food,
		after.Gold - expect.Gold,
	} {
		if d > tolerance {
			return true // gained more than expected: flag
		}
	}
	return false
}
