package sim

import (
	"math"
	"time"
)

// MarchDistance returns the tile distance between two points (Euclidean,
// rounded up to whole tiles for timing).
func MarchDistance(x1, y1, x2, y2 int) float64 {
	dx := float64(x2 - x1)
	dy := float64(y2 - y1)
	d := math.Sqrt(dx*dx + dy*dy)
	if d < 1 {
		d = 1
	}
	return d
}

// MarchDuration computes how long a march takes. Speed is the slowest unit's
// tiles-per-minute; the config's per-type speeds keep siege slow and cavalry
// fast. Returns a duration with a 30-second floor so adjacent tiles still
// feel like a march.
func MarchDuration(cfg GameConfig, troops TroopCounts, tier int, x1, y1, x2, y2 int) time.Duration {
	dist := MarchDistance(x1, y1, x2, y2)
	speed := math.MaxFloat64
	slow := func(typ string, n int64) {
		if n > 0 {
			if s := cfg.Troops[typ+":"+string(rune('0'+tier))].SpeedTiles; s < speed {
				speed = s
			}
		}
	}
	slow("infantry", troops.Infantry)
	slow("cavalry", troops.Cavalry)
	slow("archers", troops.Archers)
	slow("siege", troops.Siege)
	if speed == math.MaxFloat64 {
		speed = 10 // empty march fallback; validation should reject these anyway
	}
	minutes := dist / speed
	d := time.Duration(minutes * float64(time.Minute))
	if d < 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// MarchPosition interpolates a march's tile position at time now.
// Returns x, y as floats and a progress fraction 0..1.
func MarchPosition(x1, y1, x2, y2 int, departsAt, arrivesAt, now time.Time) (float64, float64, float64) {
	total := arrivesAt.Sub(departsAt).Seconds()
	if total <= 0 {
		return float64(x2), float64(y2), 1
	}
	el := now.Sub(departsAt).Seconds()
	p := el / total
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	x := float64(x1) + (float64(x2)-float64(x1))*p
	y := float64(y1) + (float64(y2)-float64(y1))*p
	return x, y, p
}

// TrainCostAndTime computes the resource cost and training duration for a
// batch of troops. Barracks level reduces time (5% per level, floor 50%).
func TrainCostAndTime(cfg GameConfig, troops TroopCounts, tier int, barracksLevel int) (Cost, time.Duration) {
	cost := Cost{}
	var secs int64
	add := func(typ string, n int64) {
		if n <= 0 {
			return
		}
		spec := cfg.Troops[typ+":"+string(rune('0'+tier))]
		cost.Food += spec.Food * n
		cost.Wood += spec.Wood * n
		secs += spec.Seconds * n
	}
	add("infantry", troops.Infantry)
	add("cavalry", troops.Cavalry)
	add("archers", troops.Archers)
	add("siege", troops.Siege)
	mult := 1.0 - 0.05*float64(barracksLevel)
	if mult < 0.5 {
		mult = 0.5
	}
	secs = int64(float64(secs) * mult)
	cost.Seconds = secs
	return cost, time.Duration(secs) * time.Second
}
