package store

import (
	"context"
	"time"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
)

// GetCityFull returns the full city state with lazily-computed resources.
// It also refreshes the hot Redis cache (30s TTL) on the way out.
func (s *Store) GetCityFull(ctx context.Context, cityID string, now time.Time) (CityFull, error) {
	var cf CityFull
	err := s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var c City
		if err := tx.QueryRow(ctx, `
			SELECT id, player_id, world_id, name, tile_x, tile_y, level, power, shield_until, created_at
			FROM cities WHERE id=$1`, cityID).
			Scan(&c.ID, &c.PlayerID, &c.WorldID, &c.Name, &c.X, &c.Y, &c.Level, &c.Power, &c.ShieldUntil, &c.CreatedAt); err != nil {
			return err
		}
		cf.City = c
		anchor, err := loadAnchor(ctx, tx, cityID)
		if err != nil {
			return err
		}
		amounts := sim.ComputeResources(anchor, now)
		cf.Resources = CityResources{Amounts: amounts, Rates: anchor.Rates, Caps: anchor.Caps}

		rows, err := tx.Query(ctx, `SELECT id, type, level, slot, state FROM buildings WHERE city_id=$1 ORDER BY slot`, cityID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b Building
			if err := rows.Scan(&b.ID, &b.Type, &b.Level, &b.Slot, &b.State); err != nil {
				rows.Close()
				return err
			}
			cf.Buildings = append(cf.Buildings, b)
		}
		rows.Close()

		trows, err := tx.Query(ctx, `SELECT type, count FROM troops WHERE city_id=$1`, cityID)
		if err != nil {
			return err
		}
		for trows.Next() {
			var typ string
			var n int
			if err := trows.Scan(&typ, &n); err != nil {
				trows.Close()
				return err
			}
			switch typ {
			case "infantry":
				cf.Troops.Infantry = int64(n)
			case "cavalry":
				cf.Troops.Cavalry = int64(n)
			case "archers":
				cf.Troops.Archers = int64(n)
			case "siege":
				cf.Troops.Siege = int64(n)
			}
		}
		trows.Close()

		brows, err := tx.Query(ctx, `
			SELECT id, building_id, building_type, action, target_level, starts_at, ends_at, state
			FROM build_queue WHERE city_id=$1 AND state='queued' ORDER BY ends_at`, cityID)
		if err != nil {
			return err
		}
		for brows.Next() {
			var q BuildQueueItem
			if err := brows.Scan(&q.ID, &q.BuildingID, &q.BuildingType, &q.Action, &q.TargetLevel, &q.StartsAt, &q.EndsAt, &q.State); err != nil {
				brows.Close()
				return err
			}
			cf.BuildQueue = append(cf.BuildQueue, q)
		}
		brows.Close()

		qrows, err := tx.Query(ctx, `
			SELECT id, infantry, cavalry, archers, siege, tier, starts_at, ends_at, state
			FROM train_queue WHERE city_id=$1 AND state='queued' ORDER BY ends_at`, cityID)
		if err != nil {
			return err
		}
		for qrows.Next() {
			var q TrainQueueItem
			var inf, cav, arc, sie int
			if err := qrows.Scan(&q.ID, &inf, &cav, &arc, &sie, &q.Tier, &q.StartsAt, &q.EndsAt, &q.State); err != nil {
				qrows.Close()
				return err
			}
			q.Troops = sim.TroopCounts{Infantry: int64(inf), Cavalry: int64(cav), Archers: int64(arc), Siege: int64(sie)}
			cf.TrainQueue = append(cf.TrainQueue, q)
		}
		qrows.Close()

		cfg, err := s.WorldConfig(ctx, c.WorldID)
		if err != nil {
			return err
		}
		cf.MarchSlots = cfg.MarchSlotsBase
		return nil
	})
	return cf, err
}

// CityOwner returns the player id that owns a city.
func (s *Store) CityOwner(ctx context.Context, q Querier, cityID string) (playerID string, worldID int, err error) {
	err = q.QueryRow(ctx, `SELECT player_id, world_id FROM cities WHERE id=$1`, cityID).Scan(&playerID, &worldID)
	return playerID, worldID, err
}

// TouchCityPower recomputes a city's power number (troops + buildings).
func (s *Store) TouchCityPower(ctx context.Context, q Querier, cfg sim.GameConfig, cityID string) error {
	var power float64
	rows, err := q.Query(ctx, `SELECT type, level FROM buildings WHERE city_id=$1`, cityID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var typ string
		var lvl int
		_ = rows.Scan(&typ, &lvl)
		power += float64(lvl) * 50
	}
	rows.Close()
	trows, err := q.Query(ctx, `SELECT type, count FROM troops WHERE city_id=$1`, cityID)
	if err != nil {
		return err
	}
	for trows.Next() {
		var typ string
		var n int
		_ = trows.Scan(&typ, &n)
		power += float64(n) * cfg.Troops[typ+":1"].Power
	}
	trows.Close()
	_, err = q.Exec(ctx, `UPDATE cities SET power=$2 WHERE id=$1`, cityID, int64(power))
	return err
}
