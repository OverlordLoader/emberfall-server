package sim

import "math/rand"

// SummonResult is one commander pull.
type SummonResult struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Rarity string `json:"rarity"` // common|rare|epic
}

// Commander pools. Names are flavor; stats are data-driven later.
var commonCommanders = []SummonResult{
	{Key: "ash_captain", Name: "Ash Captain Rurik", Rarity: "common"},
	{Key: "frost_scout", Name: "Frost Scout Sella", Rarity: "common"},
	{Key: "cinder_sergeant", Name: "Cinder Sergeant Bram", Rarity: "common"},
	{Key: "ember_archer", Name: "Ember Archer Nyx", Rarity: "common"},
}
var rareCommanders = []SummonResult{
	{Key: "flamewarden", Name: "Flamewarden Kael", Rarity: "rare"},
	{Key: "glacier_mage", Name: "Glacier Mage Yrsa", Rarity: "rare"},
	{Key: "obsidian_knight", Name: "Obsidian Knight Dain", Rarity: "rare"},
}
var epicCommanders = []SummonResult{
	{Key: "pyre_queen", Name: "Pyre Queen Ignis", Rarity: "epic"},
	{Key: "winter_king", Name: "Winter King Skald", Rarity: "epic"},
}

// CommanderPowerBonus returns the army multiplier a commander grants.
// Generous in phase 1: even commons help.
func CommanderPowerBonus(rarity string, stars int) float64 {
	base := map[string]float64{"common": 1.05, "rare": 1.12, "epic": 1.25}[rarity]
	return base + 0.03*float64(stars) // stars 0..5
}

// Summon rolls one commander server-side. The pity counter increments on
// non-epic pulls and resets on epic; at pityAt the pull is forced epic.
// The client never rolls — this runs in the summon handler / sweeper only.
func Summon(cfg GameConfig, rng *rand.Rand, pity int) (SummonResult, int) {
	roll := rng.Float64()
	var pool []SummonResult
	var rarity string
	if pity+1 >= cfg.GachaPityAt {
		rarity = "epic"
	} else {
		rates := cfg.GachaRates
		switch {
		case roll < rates["epic"]:
			rarity = "epic"
		case roll < rates["epic"]+rates["rare"]:
			rarity = "rare"
		default:
			rarity = "common"
		}
	}
	switch rarity {
	case "epic":
		pool = epicCommanders
	case "rare":
		pool = rareCommanders
	default:
		pool = commonCommanders
	}
	pick := pool[rng.Intn(len(pool))]
	newPity := pity + 1
	if rarity == "epic" {
		newPity = 0
	}
	return pick, newPity
}

// DailyQuest is one day's quest definition (data-driven, generous).
type DailyQuest struct {
	Key         string         `json:"key"`
	Description string         `json:"description"`
	Target      int            `json:"target"`
	Reward      ResourceBundle `json:"reward"`
	RewardItems map[string]int `json:"reward_items,omitempty"` // item_key -> qty
}

// DailyQuests returns the rotating daily quest set. Quest progress is tracked
// per player per day in the store; this is the catalog.
func DailyQuests() []DailyQuest {
	return []DailyQuest{
		{Key: "build_2", Description: "Start or finish 2 construction projects", Target: 2,
			Reward: ResourceBundle{Wood: 500, Stone: 500}, RewardItems: map[string]int{"speedup_small": 1}},
		{Key: "train_50", Description: "Train 50 troops", Target: 50,
			Reward: ResourceBundle{Food: 800}, RewardItems: map[string]int{"speedup_small": 1}},
		{Key: "march_1", Description: "Send 1 march", Target: 1,
			Reward: ResourceBundle{Gold: 200}, RewardItems: map[string]int{"speedup_small": 1}},
		{Key: "gather_1", Description: "Complete 1 gathering march", Target: 1,
			Reward: ResourceBundle{Wood: 600, Stone: 600, Food: 600}},
	}
}

// WelcomeTrack is the 7-day newcomer calendar. Claim one day at a time, in
// order; missed days can be claimed late (no punishment mechanics).
func WelcomeTrack() []DailyQuest {
	return []DailyQuest{
		{Key: "welcome_1", Description: "Day 1 — Founder's Cache", Target: 1,
			Reward:      ResourceBundle{Wood: 2000, Stone: 2000, Food: 2000, Gold: 1000},
			RewardItems: map[string]int{"speedup_small": 3}},
		{Key: "welcome_2", Description: "Day 2 — Warm Banners", Target: 1,
			Reward: ResourceBundle{Wood: 1500, Food: 1500}, RewardItems: map[string]int{"speedup_large": 1}},
		{Key: "welcome_3", Description: "Day 3 — Commander's Oath", Target: 1,
			Reward: ResourceBundle{Gold: 800}, RewardItems: map[string]int{"summon_ticket": 1}},
		{Key: "welcome_4", Description: "Day 4 — Stone and Steel", Target: 1,
			Reward: ResourceBundle{Stone: 3000, Wood: 2000}, RewardItems: map[string]int{"speedup_small": 3}},
		{Key: "welcome_5", Description: "Day 5 — Harvest Moon", Target: 1,
			Reward: ResourceBundle{Food: 4000}, RewardItems: map[string]int{"speedup_large": 1}},
		{Key: "welcome_6", Description: "Day 6 — War Council", Target: 1,
			Reward: ResourceBundle{Gold: 1200}, RewardItems: map[string]int{"summon_ticket": 2}},
		{Key: "welcome_7", Description: "Day 7 — Ember Crown", Target: 1,
			Reward:      ResourceBundle{Wood: 3000, Stone: 3000, Food: 3000, Gold: 2000},
			RewardItems: map[string]int{"speedup_large": 2, "summon_ticket": 1}},
	}
}
