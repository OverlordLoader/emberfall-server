package sim

import "time"

// ResourceBundle is a set of the four core resources (whole units).
type ResourceBundle struct {
	Wood  int64 `json:"wood"`
	Stone int64 `json:"stone"`
	Food  int64 `json:"food"`
	Gold  int64 `json:"gold"`
}

// RateBundle is per-second production rates (fractional).
type RateBundle struct {
	Wood  float64 `json:"wood"`
	Stone float64 `json:"stone"`
	Food  float64 `json:"food"`
	Gold  float64 `json:"gold"`
}

// ResourceAnchor is the lazy-computation anchor stored in city_resources.
// On any read: amount = min(cap, amount + rate*(now - updated_at)).
type ResourceAnchor struct {
	Amounts ResourceBundle // last persisted amounts
	Rates   RateBundle     // per-second rates derived from buildings
	Caps    ResourceBundle // storage caps
	Updated time.Time      // anchor time
}

// ComputeResources resolves the lazy anchor to current amounts at time now.
// Pure function: same inputs always give same outputs.
func ComputeResources(a ResourceAnchor, now time.Time) ResourceBundle {
	dt := now.Sub(a.Updated).Seconds()
	if dt < 0 {
		dt = 0
	}
	add := func(amt int64, rate float64, cap int64) int64 {
		v := amt + int64(rate*dt)
		if v > cap {
			v = cap
		}
		return v
	}
	return ResourceBundle{
		Wood:  add(a.Amounts.Wood, a.Rates.Wood, a.Caps.Wood),
		Stone: add(a.Amounts.Stone, a.Rates.Stone, a.Caps.Stone),
		Food:  add(a.Amounts.Food, a.Rates.Food, a.Caps.Food),
		Gold:  add(a.Amounts.Gold, a.Rates.Gold, a.Caps.Gold),
	}
}

// Reanchor moves the anchor forward to now, folding accrued resources in.
// Call this whenever mutating resources (spend, grant) inside a transaction.
func Reanchor(a ResourceAnchor, now time.Time) ResourceAnchor {
	a.Amounts = ComputeResources(a, now)
	a.Updated = now
	return a
}

// CanAfford reports whether amounts cover cost.
func CanAfford(amounts ResourceBundle, cost Cost) bool {
	return amounts.Wood >= cost.Wood &&
		amounts.Stone >= cost.Stone &&
		amounts.Food >= cost.Food &&
		amounts.Gold >= cost.Gold
}

// Spend subtracts cost from amounts. Caller must have checked CanAfford.
func Spend(amounts ResourceBundle, cost Cost) ResourceBundle {
	return ResourceBundle{
		Wood:  amounts.Wood - cost.Wood,
		Stone: amounts.Stone - cost.Stone,
		Food:  amounts.Food - cost.Food,
		Gold:  amounts.Gold - cost.Gold,
	}
}

// Grant adds bundle to amounts, clamped by caps.
func Grant(amounts, caps, grant ResourceBundle) ResourceBundle {
	add := func(amt, cap, g int64) int64 {
		v := amt + g
		if v > cap {
			v = cap
		}
		return v
	}
	return ResourceBundle{
		Wood:  add(amounts.Wood, caps.Wood, grant.Wood),
		Stone: add(amounts.Stone, caps.Stone, grant.Stone),
		Food:  add(amounts.Food, caps.Food, grant.Food),
		Gold:  add(amounts.Gold, caps.Gold, grant.Gold),
	}
}

// RatesForCity derives per-second rates and caps from building levels.
// Levels maps building key -> level.
func RatesForCity(cfg GameConfig, levels map[string]int) (RateBundle, ResourceBundle) {
	rates := RateBundle{}
	caps := cfg.CityFoundCaps
	townHall := levels["town_hall"]
	capMult := 1.0 + float64(townHall)*cfg.Buildings["town_hall"].CapPctPerLevel
	caps = ResourceBundle{
		Wood:  int64(float64(caps.Wood) * capMult),
		Stone: int64(float64(caps.Stone) * capMult),
		Food:  int64(float64(caps.Food) * capMult),
		Gold:  int64(float64(caps.Gold) * capMult),
	}
	rates.Wood = cfg.Buildings["lumber_mill"].RatePerSecPerLevel * float64(levels["lumber_mill"])
	rates.Stone = cfg.Buildings["quarry"].RatePerSecPerLevel * float64(levels["quarry"])
	rates.Food = cfg.Buildings["farm"].RatePerSecPerLevel * float64(levels["farm"])
	rates.Gold = cfg.Buildings["gold_mine"].RatePerSecPerLevel * float64(levels["gold_mine"])
	return rates, caps
}
