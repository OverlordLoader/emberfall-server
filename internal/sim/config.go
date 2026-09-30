// Package sim contains pure game logic: no I/O, no DB, no network.
// Every function is a deterministic function of its inputs so the sweeper,
// admin tools, and unit tests all share one code path for outcomes.
// The client never computes outcomes.
package sim

import "math"

// Cost is what an action spends, plus how long it takes.
type Cost struct {
	Wood    int64 `json:"wood"`
	Stone   int64 `json:"stone"`
	Food    int64 `json:"food"`
	Gold    int64 `json:"gold"`
	Seconds int64 `json:"seconds"`
}

func (c Cost) ToBundle() ResourceBundle {
	return ResourceBundle{Wood: c.Wood, Stone: c.Stone, Food: c.Food, Gold: c.Gold}
}

// BuildingSpec describes one building type's economy curve.
// Cost at level L = Base * Growth^(L-1), rounded.
type BuildingSpec struct {
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	MaxLevel    int     `json:"max_level"`
	BaseWood    int64   `json:"base_wood"`
	BaseStone   int64   `json:"base_stone"`
	BaseFood    int64   `json:"base_food"`
	BaseGold    int64   `json:"base_gold"`
	CostGrowth  float64 `json:"cost_growth"`
	BaseSeconds int64   `json:"base_seconds"`
	TimeGrowth  float64 `json:"time_growth"`
	// Effect: rate added per level for the resource buildings, cap% for town hall,
	// defense% for wall, scout tiles for watchtower, training speed% for barracks.
	RatePerSecPerLevel float64 `json:"rate_per_sec_per_level"`
	CapPctPerLevel     float64 `json:"cap_pct_per_level"`
	DefensePctPerLevel float64 `json:"defense_pct_per_level"`
	ScoutPerLevel      int     `json:"scout_per_level"`
}

// TroopSpec is the stat block for one troop type at one tier.
type TroopSpec struct {
	Type       string  `json:"type"` // infantry|cavalry|archers|siege
	Tier       int     `json:"tier"`
	Name       string  `json:"name"`
	Power      float64 `json:"power"`       // per-unit combat power
	Carry      int64   `json:"carry"`       // resources carried per unit (plunder)
	SpeedTiles float64 `json:"speed_tiles"` // tiles per minute on the march
	Food       int64   `json:"food"`        // food cost per unit
	Wood       int64   `json:"wood"`
	Seconds    int64   `json:"seconds"`    // training seconds per unit
	PowerMult  float64 `json:"power_mult"` // tier multiplier baked in for convenience
}

// GameConfig is the full data-driven tuning for one world. Worlds carry their
// own copy (JSONB) so themed variants are content packs, not code forks.
type GameConfig struct {
	Version   string                  `json:"version"`
	Buildings map[string]BuildingSpec `json:"buildings"`
	Troops    map[string]TroopSpec    `json:"troops"` // key "infantry:1" etc.
	// Tuning knobs
	MarchSlotsBase      int                `json:"march_slots_base"`
	GatherRatePerMin    float64            `json:"gather_rate_per_min"`
	TriangleBonus       float64            `json:"triangle_bonus"` // 1.25
	JitterPct           float64            `json:"jitter_pct"`     // 0.10
	ScoutRangeBase      int                `json:"scout_range_base"`
	CityFoundResources  ResourceBundle     `json:"city_found_resources"`
	CityFoundCaps       ResourceBundle     `json:"city_found_caps"`
	GachaRates          map[string]float64 `json:"gacha_rates"` // common|rare|epic
	GachaPityAt         int                `json:"gacha_pity_at"`
	SpeedupSecondsSmall int64              `json:"speedup_seconds_small"`
	SpeedupSecondsLarge int64              `json:"speedup_seconds_large"`
}

func troopKey(t string, tier int) string {
	return t + ":" + string(rune('0'+tier))
}

// DefaultConfig returns the Emberfall (fire/frost) tuning.
func DefaultConfig() GameConfig {
	growth := 1.45
	timeGrowth := 1.5
	mk := func(key, name string, max int, w, s, f, g int64, secs int64, rate, capPct, defPct float64, scout int) BuildingSpec {
		return BuildingSpec{
			Key: key, Name: name, MaxLevel: max,
			BaseWood: w, BaseStone: s, BaseFood: f, BaseGold: g,
			CostGrowth: growth, BaseSeconds: secs, TimeGrowth: timeGrowth,
			RatePerSecPerLevel: rate, CapPctPerLevel: capPct,
			DefensePctPerLevel: defPct, ScoutPerLevel: scout,
		}
	}
	buildings := map[string]BuildingSpec{
		// The 8 phase-1 building types.
		"town_hall":   mk("town_hall", "Ember Hall", 25, 200, 200, 100, 50, 120, 0, 0.20, 0, 0),
		"farm":        mk("farm", "Ash Farm", 25, 100, 60, 0, 20, 60, 0.010, 0, 0, 0),            // food/s
		"lumber_mill": mk("lumber_mill", "Cinder Mill", 25, 60, 100, 40, 20, 60, 0.008, 0, 0, 0), // wood/s
		"quarry":      mk("quarry", "Frost Quarry", 25, 80, 80, 60, 20, 60, 0.006, 0, 0, 0),      // stone/s
		"gold_mine":   mk("gold_mine", "Ember Vein", 25, 150, 150, 100, 0, 120, 0.004, 0, 0, 0),  // gold/s
		"barracks":    mk("barracks", "War Barracks", 25, 200, 150, 100, 80, 180, 0, 0, 0, 0),
		"wall":        mk("wall", "Obsidian Wall", 25, 250, 300, 100, 100, 240, 0, 0, 0.04, 0),
		"watchtower":  mk("watchtower", "Farseer Tower", 25, 120, 120, 80, 60, 90, 0, 0, 0, 2),
	}
	troops := map[string]TroopSpec{}
	addT := func(t, name string, tier int, power float64, carry int64, speed float64, food, wood, secs int64) {
		key := t + ":" + string(rune('0'+tier))
		mults := map[int]float64{1: 1.0, 2: 1.6, 3: 2.4}
		troops[key] = TroopSpec{
			Type: t, Tier: tier, Name: name,
			Power: power * mults[tier], Carry: carry, SpeedTiles: speed,
			Food: food * int64(tier), Wood: wood * int64(tier),
			Seconds: secs, PowerMult: mults[tier],
		}
	}
	for _, tier := range []int{1, 2, 3} {
		addT("infantry", "Emberguard", tier, 12, 20, 12, 10, 5, 20)
		addT("cavalry", "Cinder Rider", tier, 14, 30, 20, 15, 10, 30)
		addT("archers", "Frostbow", tier, 12, 15, 14, 12, 8, 25)
		addT("siege", "Magma Ram", tier, 30, 10, 6, 40, 30, 90)
	}
	return GameConfig{
		Version:             "emberfall-1.0",
		Buildings:           buildings,
		Troops:              troops,
		MarchSlotsBase:      2,
		GatherRatePerMin:    50,
		TriangleBonus:       1.25,
		JitterPct:           0.10,
		ScoutRangeBase:      6,
		CityFoundResources:  ResourceBundle{Wood: 800, Stone: 800, Food: 800, Gold: 400},
		CityFoundCaps:       ResourceBundle{Wood: 5000, Stone: 5000, Food: 5000, Gold: 2500},
		GachaRates:          map[string]float64{"common": 0.70, "rare": 0.25, "epic": 0.05},
		GachaPityAt:         40,
		SpeedupSecondsSmall: 300,
		SpeedupSecondsLarge: 3600,
	}
}

// BuildCost returns the cost + duration to take a building from fromLevel to
// fromLevel+1 (level numbers are 1-based; fromLevel=0 means new construction).
func BuildCost(cfg GameConfig, spec BuildingSpec, fromLevel int) Cost {
	g := spec.CostGrowth
	lvl := float64(fromLevel) // new build at level 1 uses growth^0
	pow := func(base int64) int64 { return int64(float64(base)*powf(g, lvl) + 0.5) }
	return Cost{
		Wood:    pow(spec.BaseWood),
		Stone:   pow(spec.BaseStone),
		Food:    pow(spec.BaseFood),
		Gold:    pow(spec.BaseGold),
		Seconds: int64(float64(spec.BaseSeconds)*powf(spec.TimeGrowth, lvl) + 0.5),
	}
}

// powf is math.Pow, kept as a named helper for readability.
func powf(base, exp float64) float64 {
	return math.Pow(base, exp)
}

// ScoutRange returns how far a player can see details, in tiles.
func ScoutRange(cfg GameConfig, watchtowerLevel int) int {
	return cfg.ScoutRangeBase + watchtowerLevel*cfg.Buildings["watchtower"].ScoutPerLevel
}

// WallDefenseBonus returns the defender power multiplier from wall level.
func WallDefenseBonus(cfg GameConfig, wallLevel int) float64 {
	return 1.0 + float64(wallLevel)*cfg.Buildings["wall"].DefensePctPerLevel
}
