package sim

import (
	"math/rand"
	"testing"
	"time"
)

func TestLazyResources(t *testing.T) {
	anchor := ResourceAnchor{
		Amounts: ResourceBundle{Wood: 100},
		Rates:   RateBundle{Wood: 1.0},
		Caps:    ResourceBundle{Wood: 150},
		Updated: time.Now().Add(-100 * time.Second),
	}
	got := ComputeResources(anchor, time.Now())
	if got.Wood != 150 { // capped: 100 + 100 = 200 -> 150
		t.Fatalf("expected cap 150, got %d", got.Wood)
	}
	// Deterministic: same inputs, same output.
	got2 := ComputeResources(anchor, anchor.Updated.Add(100*time.Second))
	if got2.Wood != 150 {
		t.Fatalf("expected 150 deterministic, got %d", got2.Wood)
	}
}

func TestBuildCostGrowth(t *testing.T) {
	cfg := DefaultConfig()
	spec := cfg.Buildings["farm"]
	c1 := BuildCost(cfg, spec, 0)
	c5 := BuildCost(cfg, spec, 4)
	if c5.Wood <= c1.Wood {
		t.Fatalf("costs should grow with level: %d vs %d", c1.Wood, c5.Wood)
	}
	if c1.Seconds <= 0 {
		t.Fatal("build time must be positive")
	}
}

func TestBattleDeterminism(t *testing.T) {
	cfg := DefaultConfig()
	atk := Army{Troops: TroopCounts{Infantry: 100, Cavalry: 50}, Tier: 1, CommanderMult: 1.1, TechMult: 1}
	def := Army{Troops: TroopCounts{Infantry: 80, Archers: 40}, Tier: 1, CommanderMult: 1, TechMult: 1}
	stored := ResourceBundle{Wood: 1000}
	r1 := ResolveBattle(cfg, atk, def, 1.2, stored, 42)
	r2 := ResolveBattle(cfg, atk, def, 1.2, stored, 42)
	if r1.AttackerPower != r2.AttackerPower || r1.AttackerWon != r2.AttackerWon ||
		r1.AttackerLeft.Infantry != r2.AttackerLeft.Infantry {
		t.Fatal("battle must be deterministic for the same seed")
	}
	// Different seeds may differ (jitter) but powers must match.
	r3 := ResolveBattle(cfg, atk, def, 1.2, stored, 43)
	if r3.AttackerPower != r1.AttackerPower {
		t.Fatal("power must not depend on seed")
	}
	// Triangle: pure infantry beats pure cavalry of equal raw power.
	inf := Army{Troops: TroopCounts{Infantry: 100}, Tier: 1, CommanderMult: 1, TechMult: 1}
	cav := Army{Troops: TroopCounts{Cavalry: 100}, Tier: 1, CommanderMult: 1, TechMult: 1}
	b := ResolveBattle(cfg, inf, cav, 1.0, ResourceBundle{}, 7)
	if !b.AttackerWon {
		t.Fatal("infantry should beat cavalry via triangle bonus")
	}
}

func TestGachaPity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GachaPityAt = 5
	rng := rand.New(rand.NewSource(1))
	pity := 0
	sawEpic := false
	for i := 0; i < 10; i++ {
		res, np := Summon(cfg, rng, pity)
		pity = np
		if res.Rarity == "epic" {
			sawEpic = true
			if pity != 0 {
				t.Fatal("pity must reset on epic")
			}
		}
	}
	if !sawEpic {
		t.Fatal("pity should force an epic within 5 pulls")
	}
}

func TestMarchDuration(t *testing.T) {
	cfg := DefaultConfig()
	d := MarchDuration(cfg, TroopCounts{Cavalry: 10}, 1, 0, 0, 20, 0)
	if d < 30*time.Second {
		t.Fatal("march has a 30s floor")
	}
	siege := MarchDuration(cfg, TroopCounts{Siege: 10}, 1, 0, 0, 20, 0)
	if siege <= d {
		t.Fatal("siege should march slower than cavalry")
	}
}

func TestSpendGrant(t *testing.T) {
	amt := ResourceBundle{Wood: 100, Stone: 100, Food: 100, Gold: 100}
	cost := Cost{Wood: 60, Stone: 200}
	if CanAfford(amt, cost) {
		t.Fatal("should not afford")
	}
	cost2 := Cost{Wood: 60}
	if !CanAfford(amt, cost2) {
		t.Fatal("should afford")
	}
	left := Spend(amt, cost2)
	if left.Wood != 40 {
		t.Fatalf("expected 40 wood, got %d", left.Wood)
	}
	caps := ResourceBundle{Wood: 50, Stone: 200, Food: 200, Gold: 200}
	got := Grant(left, caps, ResourceBundle{Wood: 100})
	if got.Wood != 50 {
		t.Fatalf("grant should clamp to cap, got %d", got.Wood)
	}
}

func TestSummonRatesShape(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(99))
	counts := map[string]int{}
	for i := 0; i < 2000; i++ {
		res, _ := Summon(cfg, rng, 0)
		counts[res.Rarity]++
	}
	// Loose statistical sanity: commons should dominate.
	if counts["common"] < 1000 {
		t.Fatalf("commons too rare: %v", counts)
	}
	if counts["epic"] > 300 {
		t.Fatalf("epics too common: %v", counts)
	}
}

func TestWelcomeAndQuests(t *testing.T) {
	if len(WelcomeTrack()) != 7 {
		t.Fatal("welcome track must be 7 days")
	}
	if len(DailyQuests()) == 0 {
		t.Fatal("need daily quests")
	}
}
