// Command simtest exercises the pure sim package deterministically: it runs a
// scripted battle, a gacha pity run, and a resource-lazy-compute check, and
// prints the results. No DB, no network — the same code path the sweeper uses.
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
)

func main() {
	cfg := sim.DefaultConfig()
	fmt.Printf("config: %s (%d buildings, %d troop specs)\n", cfg.Version, len(cfg.Buildings), len(cfg.Troops))

	// 1. Scripted PvE battle: 120 T1 infantry + 40 T1 cavalry vs a frost node garrison.
	atk := sim.Army{Troops: sim.TroopCounts{Infantry: 120, Cavalry: 40}, Tier: 1, CommanderMult: 1.12, TechMult: 1.0}
	def := sim.Army{Troops: sim.TroopCounts{Infantry: 60, Archers: 60}, Tier: 1, CommanderMult: 1.0, TechMult: 1.0}
	res := sim.ResolveBattle(cfg, atk, def, 1.0, sim.ResourceBundle{Wood: 4000}, 1234)
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Printf("\nbattle (seed 1234):\n%s\n", out)

	// 2. Determinism check: same seed must give identical results.
	res2 := sim.ResolveBattle(cfg, atk, def, 1.0, sim.ResourceBundle{Wood: 4000}, 1234)
	if res.AttackerLeft != res2.AttackerLeft || res.AttackerWon != res2.AttackerWon {
		panic("battle is not deterministic!")
	}
	fmt.Println("determinism: OK (same seed -> same outcome)")

	// 3. Gacha pity: force the pity path with a low threshold.
	pityCfg := cfg
	pityCfg.GachaPityAt = 5
	rng := rand.New(rand.NewSource(7))
	pity := 0
	for i := 0; i < 8; i++ {
		r, np := sim.Summon(pityCfg, rng, pity)
		pity = np
		fmt.Printf("pull %d: %-22s %-6s (pity now %d)\n", i+1, r.Name, r.Rarity, pity)
	}

	// 4. Lazy resources: 1 hour of farm production, clamped by cap.
	now := time.Now()
	anchor := sim.ResourceAnchor{
		Amounts: sim.ResourceBundle{Food: 100},
		Rates:   sim.RateBundle{Food: 0.05}, // 180/hr
		Caps:    sim.ResourceBundle{Food: 250},
		Updated: now.Add(-time.Hour),
	}
	got := sim.ComputeResources(anchor, now)
	fmt.Printf("\nlazy resources after 1h: food=%d (cap 250, expect 250)\n", got.Food)

	// 5. Build cost curve for town_hall 1 -> 5.
	spec := cfg.Buildings["town_hall"]
	for lvl := 0; lvl < 5; lvl++ {
		c := sim.BuildCost(cfg, spec, lvl)
		fmt.Printf("town_hall %d->%d: wood=%d stone=%d food=%d gold=%d secs=%d\n",
			lvl+1, lvl+2, c.Wood, c.Stone, c.Food, c.Gold, c.Seconds)
	}

	// 6. March duration sanity.
	d := sim.MarchDuration(cfg, sim.TroopCounts{Cavalry: 50}, 1, 0, 0, 30, 40)
	fmt.Printf("\nmarch 50 tiles (cavalry): %s\n", d.Round(time.Second))
}
