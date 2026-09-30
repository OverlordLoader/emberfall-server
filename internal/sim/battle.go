package sim

import (
	"math"
	"math/rand"
)

// TroopCounts is the four troop types. Siege is excluded from the triangle
// (it is anti-building) but contributes raw power.
type TroopCounts struct {
	Infantry int64 `json:"infantry"`
	Cavalry  int64 `json:"cavalry"`
	Archers  int64 `json:"archers"`
	Siege    int64 `json:"siege"`
}

func (t TroopCounts) Total() int64 {
	return t.Infantry + t.Cavalry + t.Archers + t.Siege
}

func (t TroopCounts) ApplyLosses(pct float64) TroopCounts {
	l := func(c int64) int64 {
		lost := int64(float64(c) * pct)
		if lost > c {
			lost = c
		}
		return c - lost
	}
	return TroopCounts{
		Infantry: l(t.Infantry),
		Cavalry:  l(t.Cavalry),
		Archers:  l(t.Archers),
		Siege:    l(t.Siege),
	}
}

// Army is one side of a battle: troops at a given tier with multipliers.
type Army struct {
	Troops        TroopCounts
	Tier          int     // 1|2|3
	CommanderMult float64 // 1.0 + commander bonus, e.g. 1.15
	TechMult      float64 // reserved for alliance tech (phase 2), 1.0 in phase 1
}

// BattleResult is the deterministic outcome of ResolveBattle.
type BattleResult struct {
	AttackerPower   float64        `json:"attacker_power"`
	DefenderPower   float64        `json:"defender_power"`
	AttackerWon     bool           `json:"attacker_won"`
	AttackerLossPct float64        `json:"attacker_loss_pct"`
	DefenderLossPct float64        `json:"defender_loss_pct"`
	AttackerLeft    TroopCounts    `json:"attacker_left"`
	DefenderLeft    TroopCounts    `json:"defender_left"`
	Plunder         ResourceBundle `json:"plunder"`
}

// triangleBonus returns the damage multiplier of attacker type atkType vs
// defender type defType. infantry > cavalry > archers > infantry.
func triangleBonus(triangle float64, atkType, defType string) float64 {
	wins := map[string]string{"infantry": "cavalry", "cavalry": "archers", "archers": "infantry"}
	if wins[atkType] == defType {
		return triangle
	}
	return 1.0
}

var troopTypes = []string{"infantry", "cavalry", "archers", "siege"}

// effectivePower computes power of atk troops against a defender composition,
// weighting the triangle bonus by the defender's troop mix.
func effectivePower(cfg GameConfig, atk TroopCounts, tier int, atkMult float64, defMix map[string]float64) float64 {
	get := func(t TroopCounts, typ string) int64 {
		switch typ {
		case "infantry":
			return t.Infantry
		case "cavalry":
			return t.Cavalry
		case "archers":
			return t.Archers
		case "siege":
			return t.Siege
		}
		return 0
	}
	total := 0.0
	for _, at := range troopTypes {
		n := float64(get(atk, at))
		if n == 0 {
			continue
		}
		unitPower := cfg.Troops[at+":"+string(rune('0'+tier))].Power
		// Weighted triangle bonus across the defender mix.
		bonus := 0.0
		for _, dt := range troopTypes {
			share := defMix[dt]
			bonus += share * triangleBonus(cfg.TriangleBonus, at, dt)
		}
		total += n * unitPower * atkMult * bonus
	}
	return total
}

// ResolveBattle resolves one battle deterministically. Same inputs (including
// seed) always produce the same output. Used by the sweeper and admin tooling
// alike — the client never rolls this.
func ResolveBattle(cfg GameConfig, atk Army, def Army, defWallBonus float64,
	defenderStored ResourceBundle, seed int64) BattleResult {

	atkMult := atk.CommanderMult * atk.TechMult
	defMult := def.CommanderMult * def.TechMult * defWallBonus

	defTotal := float64(def.Troops.Total())
	defMix := map[string]float64{}
	if defTotal > 0 {
		defMix["infantry"] = float64(def.Troops.Infantry) / defTotal
		defMix["cavalry"] = float64(def.Troops.Cavalry) / defTotal
		defMix["archers"] = float64(def.Troops.Archers) / defTotal
		defMix["siege"] = float64(def.Troops.Siege) / defTotal
	} else {
		defMix = map[string]float64{"infantry": 0.25, "cavalry": 0.25, "archers": 0.25, "siege": 0.25}
	}
	atkTotal := float64(atk.Troops.Total())
	atkMix := map[string]float64{}
	if atkTotal > 0 {
		atkMix["infantry"] = float64(atk.Troops.Infantry) / atkTotal
		atkMix["cavalry"] = float64(atk.Troops.Cavalry) / atkTotal
		atkMix["archers"] = float64(atk.Troops.Archers) / atkTotal
		atkMix["siege"] = float64(atk.Troops.Siege) / atkTotal
	} else {
		atkMix = map[string]float64{"infantry": 0.25, "cavalry": 0.25, "archers": 0.25, "siege": 0.25}
	}

	atkPower := effectivePower(cfg, atk.Troops, atk.Tier, atkMult, defMix)
	defPower := effectivePower(cfg, def.Troops, def.Tier, defMult, atkMix)

	rng := rand.New(rand.NewSource(seed))
	jitter := func() float64 {
		return 1.0 + (rng.Float64()*2-1)*cfg.JitterPct
	}

	denom := atkPower + defPower
	r := 0.5
	if denom > 0 {
		r = atkPower / denom
	}
	attackerLossPct := (0.05 + 0.65*(1-r)) * jitter()
	defenderLossPct := (0.05 + 0.65*r) * jitter()
	attackerLossPct = math.Min(attackerLossPct, 1.0)
	defenderLossPct = math.Min(defenderLossPct, 1.0)

	attackerWon := r > 0.5

	// Plunder: winner takes a share of the defender's stored resources, capped
	// by the attacker's carry capacity.
	carryCap := CarryCapacity(cfg, atk.Troops, atk.Tier)
	plunderShare := 0.25 // winners take 25% of stored
	plunder := ResourceBundle{}
	if attackerWon {
		tryTake := func(stored int64) int64 {
			take := int64(float64(stored) * plunderShare)
			if take > carryCap {
				take = carryCap
			}
			carryCap -= take
			if carryCap < 0 {
				carryCap = 0
			}
			return take
		}
		plunder.Wood = tryTake(defenderStored.Wood)
		plunder.Stone = tryTake(defenderStored.Stone)
		plunder.Food = tryTake(defenderStored.Food)
		plunder.Gold = tryTake(defenderStored.Gold)
	}

	return BattleResult{
		AttackerPower:   atkPower,
		DefenderPower:   defPower,
		AttackerWon:     attackerWon,
		AttackerLossPct: attackerLossPct,
		DefenderLossPct: defenderLossPct,
		AttackerLeft:    atk.Troops.ApplyLosses(attackerLossPct),
		DefenderLeft:    def.Troops.ApplyLosses(defenderLossPct),
		Plunder:         plunder,
	}
}

// CarryCapacity is the total resources an army can haul home.
func CarryCapacity(cfg GameConfig, t TroopCounts, tier int) int64 {
	cap := int64(0)
	add := func(typ string, n int64) {
		if n > 0 {
			cap += cfg.Troops[typ+":"+string(rune('0'+tier))].Carry * n
		}
	}
	add("infantry", t.Infantry)
	add("cavalry", t.Cavalry)
	add("archers", t.Archers)
	add("siege", t.Siege)
	return cap
}
