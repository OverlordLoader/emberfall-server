package sim

import (
	"math/rand"
	"testing"
)

func TestSummonEpic(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		res := SummonEpic(rng)
		if res.Rarity != "epic" {
			t.Fatalf("SummonEpic returned non-epic rarity %q", res.Rarity)
		}
		if res.Key == "" || res.Name == "" {
			t.Fatalf("SummonEpic returned empty key/name: %+v", res)
		}
		seen[res.Key] = true
	}
	// Both epics in the pool should appear over 200 pulls (sanity: pool is used).
	if len(seen) != len(epicCommanders) {
		t.Fatalf("expected all %d epic commanders to appear, saw %d", len(epicCommanders), len(seen))
	}
}
