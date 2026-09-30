package store

import (
	"context"
	"strings"
	"testing"
)

// The server's grant table must match the client's final product set exactly
// (client: Emberfall/Game/StoreManager.swift). The game is ad-free, so there
// must be no remove-ads product anywhere in this table.
func TestIAPProductGrantsMatchClient(t *testing.T) {
	want := map[string]map[string]int{
		"app.emberfall.game.bundle.speedup": {"speedup_15m": 3},
		"app.emberfall.game.summon.epic10":  {"summon_ticket": 10},
		"app.emberfall.game.bundle.warden":  {"speedup_15m": 8},
	}
	if len(IAPProductGrants) != len(want) {
		t.Fatalf("expected %d products, got %d", len(want), len(IAPProductGrants))
	}
	for id, items := range want {
		got, ok := IAPProductGrants[id]
		if !ok {
			t.Fatalf("missing product %q", id)
		}
		if len(got) != len(items) {
			t.Fatalf("product %q: expected %d item kinds, got %d", id, len(items), len(got))
		}
		for k, qty := range items {
			if got[k] != qty {
				t.Fatalf("product %q: item %q expected qty %d, got %d", id, k, qty, got[k])
			}
		}
	}
	// No remove-ads product: the game is ad-free.
	for id := range IAPProductGrants {
		if strings.Contains(id, "removeads") {
			t.Fatalf("stale remove-ads product %q must not exist", id)
		}
	}
	if n := IAPProductEpicSummons[ProductBundleWarden]; n != 3 {
		t.Fatalf("warden bundle must grant 3 epic summons, got %d", n)
	}
}

// Unknown products are rejected before any DB access (no Store needed).
func TestFulfillIAPProductUnknownRejected(t *testing.T) {
	s := &Store{}
	for _, id := range []string{
		"",
		"app.emberfall.game.removeads", // deleted product must be rejected
		"app.emberfall.game.bundle.nope",
	} {
		if err := s.FulfillIAPProduct(context.Background(), "00000000-0000-0000-0000-000000000000", id); err == nil {
			t.Fatalf("expected error for unknown product %q", id)
		}
	}
}
