package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
)

// ---- construction ----

// StartBuild validates and starts a building upgrade (or new construction at
// slot). One active build per city in phase 1 (generous, simple).
func (s *Store) StartBuild(ctx context.Context, cityID, buildingType string, slot int, now time.Time) (queueID string, endsAt time.Time, err error) {
	err = s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		owner, worldID, err := s.CityOwner(ctx, tx, cityID)
		if err != nil {
			return fmt.Errorf("city not found")
		}
		_ = owner
		cfg, err := s.WorldConfig(ctx, worldID)
		if err != nil {
			return err
		}
		spec, ok := cfg.Buildings[buildingType]
		if !ok {
			return fmt.Errorf("unknown building type")
		}
		// One active build at a time (phase 1 rule; documented).
		var active int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM build_queue WHERE city_id=$1 AND state='queued'`, cityID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("a build is already in progress")
		}
		var buildingID *string
		var curLevel int
		var bid string
		if err := tx.QueryRow(ctx,
			`SELECT id, level FROM buildings WHERE city_id=$1 AND type=$2`, cityID, buildingType).
			Scan(&bid, &curLevel); err == nil {
			buildingID = &bid
		}
		if curLevel >= spec.MaxLevel {
			return fmt.Errorf("building at max level")
		}
		// Town hall gates everything: a building can't exceed town hall level.
		var thLevel int
		_ = tx.QueryRow(ctx, `SELECT level FROM buildings WHERE city_id=$1 AND type='town_hall'`, cityID).Scan(&thLevel)
		if buildingType != "town_hall" && curLevel+1 > thLevel {
			return fmt.Errorf("upgrade town hall first")
		}
		cost := sim.BuildCost(cfg, spec, curLevel)
		anchor, err := loadAnchor(ctx, tx, cityID)
		if err != nil {
			return err
		}
		anchor = sim.Reanchor(anchor, now)
		if !sim.CanAfford(anchor.Amounts, cost) {
			return fmt.Errorf("insufficient resources")
		}
		anchor.Amounts = sim.Spend(anchor.Amounts, cost)
		if err := saveAnchor(ctx, tx, cityID, anchor); err != nil {
			return err
		}
		queueID = uuid.NewString()
		startsAt := now
		endsAt = now.Add(time.Duration(cost.Seconds) * time.Second)
		if _, err := tx.Exec(ctx, `
			INSERT INTO build_queue(id, world_id, city_id, building_id, building_type, action, target_level, starts_at, ends_at, state)
			VALUES($1,$2,$3,$4,$5,'upgrade',$6,$7,$8,'queued')`,
			queueID, worldID, cityID, buildingID, buildingType, curLevel+1, startsAt, endsAt); err != nil {
			return err
		}
		if buildingID == nil {
			// Reserve the building row in 'building' state so slots can't double-book.
			if _, err := tx.Exec(ctx,
				`INSERT INTO buildings(id, city_id, world_id, type, level, slot, state)
				 VALUES($1,$2,$3,$4,0,$5,'building')`,
				uuid.NewString(), cityID, worldID, buildingType, slot); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE buildings SET state='building' WHERE id=$1`, *buildingID); err != nil {
				return err
			}
		}
		if err := insertEvent(ctx, tx, worldID, "build_done", queueID, endsAt); err != nil {
			return err
		}
		return s.TouchCityPower(ctx, tx, cfg, cityID)
	})
	return queueID, endsAt, err
}

// CancelBuild cancels a queued build and refunds 50% (generous, no punishment).
func (s *Store) CancelBuild(ctx context.Context, queueID string, now time.Time) error {
	return s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var cityID, buildingType string
		var targetLevel int
		var worldID int
		var buildingID *string
		if err := tx.QueryRow(ctx, `
			SELECT city_id, building_id, building_type, target_level, world_id
			FROM build_queue WHERE id=$1 AND state='queued'`, queueID).
			Scan(&cityID, &buildingID, &buildingType, &targetLevel, &worldID); err != nil {
			return fmt.Errorf("build not found or not cancellable")
		}
		cfg, _ := s.WorldConfig(ctx, worldID)
		spec := cfg.Buildings[buildingType]
		cost := sim.BuildCost(cfg, spec, targetLevel-1)
		// 50% refund of each resource.
		anchor, err := loadAnchor(ctx, tx, cityID)
		if err != nil {
			return err
		}
		anchor = sim.Reanchor(anchor, now)
		refund := sim.Cost{Wood: cost.Wood / 2, Stone: cost.Stone / 2, Food: cost.Food / 2, Gold: cost.Gold / 2}
		anchor.Amounts = sim.Grant(anchor.Amounts, anchor.Caps, refund.ToBundle())
		if err := saveAnchor(ctx, tx, cityID, anchor); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE build_queue SET state='cancelled' WHERE id=$1`, queueID); err != nil {
			return err
		}
		if buildingID == nil {
			_, err = tx.Exec(ctx, `DELETE FROM buildings WHERE city_id=$1 AND type=$2 AND state='building'`, cityID, buildingType)
		} else {
			_, err = tx.Exec(ctx, `UPDATE buildings SET state='active' WHERE id=$1`, *buildingID)
		}
		return err
	})
}

// SpeedupBuild consumes a speedup item (or instant-completes when item_key is
// empty and the remaining time is trivially small — no, phase 1: item only).
// If itemKey is empty, it errors: speedups always cost an item in phase 1.
func (s *Store) SpeedupBuild(ctx context.Context, queueID, playerID, itemKey string, now time.Time) (time.Time, error) {
	var endsAt time.Time
	err := s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var cityID string
		var worldID int
		if err := tx.QueryRow(ctx,
			`SELECT city_id, world_id, ends_at FROM build_queue WHERE id=$1 AND state='queued'`, queueID).
			Scan(&cityID, &worldID, &endsAt); err != nil {
			return fmt.Errorf("build not found")
		}
		owner, _, err := s.CityOwner(ctx, tx, cityID)
		if err != nil || owner != playerID {
			return fmt.Errorf("not your city")
		}
		if itemKey == "" {
			return fmt.Errorf("item_key required")
		}
		seconds, err := consumeItem(ctx, tx, playerID, worldID, itemKey, 1)
		if err != nil {
			return err
		}
		_ = seconds
		remaining := endsAt.Sub(now)
		itemSecs, err := itemSpeedupSeconds(ctx, tx, itemKey)
		if err != nil {
			return err
		}
		if remaining <= time.Duration(itemSecs)*time.Second {
			// Item covers the rest: complete immediately via the shared path.
			if _, err := tx.Exec(ctx, `UPDATE build_queue SET state='done', ends_at=$2 WHERE id=$1`, queueID, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE scheduled_events SET status='done' WHERE kind='build_done' AND ref_id=$1`, queueID); err != nil {
				return err
			}
			endsAt = now
			return completeBuild(ctx, tx, s, queueID, now)
		}
		endsAt = endsAt.Add(-time.Duration(itemSecs) * time.Second)
		if _, err := tx.Exec(ctx, `UPDATE build_queue SET ends_at=$2 WHERE id=$1`, queueID, endsAt); err != nil {
			return err
		}
		// Move the scheduled event earlier too.
		_, err = tx.Exec(ctx, `UPDATE scheduled_events SET run_at=$2 WHERE kind='build_done' AND ref_id=$1 AND status='pending'`, queueID, endsAt)
		return err
	})
	return endsAt, err
}

// completeBuild applies a finished build_queue row. Shared by the sweeper and
// SpeedupBuild — one code path for completions.
func completeBuild(ctx context.Context, tx Querier, s *Store, queueID string, now time.Time) error {
	var cityID, buildingType string
	var targetLevel, worldID int
	var buildingID *string
	if err := tx.QueryRow(ctx, `
		SELECT city_id, building_id, building_type, target_level, world_id
		FROM build_queue WHERE id=$1`, queueID).
		Scan(&cityID, &buildingID, &buildingType, &targetLevel, &worldID); err != nil {
		return err
	}
	if buildingID == nil {
		var bid string
		if err := tx.QueryRow(ctx,
			`SELECT id FROM buildings WHERE city_id=$1 AND type=$2 AND state='building'`, cityID, buildingType).Scan(&bid); err != nil {
			return err
		}
		buildingID = &bid
	}
	if _, err := tx.Exec(ctx,
		`UPDATE buildings SET level=$2, state='active' WHERE id=$1`, *buildingID, targetLevel); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE build_queue SET state='done' WHERE id=$1`, queueID); err != nil {
		return err
	}
	cfg, _ := s.WorldConfig(ctx, worldID)
	if err := recomputeRates(ctx, tx, s, worldID, cityID, now); err != nil {
		return err
	}
	return s.TouchCityPower(ctx, tx, cfg, cityID)
}

// ---- training ----

// StartTrain validates and queues troop training.
func (s *Store) StartTrain(ctx context.Context, cityID string, troops sim.TroopCounts, tier int, now time.Time) (queueID string, endsAt time.Time, err error) {
	if troops.Total() <= 0 || troops.Total() > 100000 {
		return "", time.Time{}, fmt.Errorf("invalid troop count")
	}
	if tier < 1 || tier > 3 {
		return "", time.Time{}, fmt.Errorf("invalid tier")
	}
	err = s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		_, worldID, err := s.CityOwner(ctx, tx, cityID)
		if err != nil {
			return fmt.Errorf("city not found")
		}
		cfg, err := s.WorldConfig(ctx, worldID)
		if err != nil {
			return err
		}
		var barracks int
		_ = tx.QueryRow(ctx, `SELECT level FROM buildings WHERE city_id=$1 AND type='barracks'`, cityID).Scan(&barracks)
		cost, dur := sim.TrainCostAndTime(cfg, troops, tier, barracks)
		anchor, err := loadAnchor(ctx, tx, cityID)
		if err != nil {
			return err
		}
		anchor = sim.Reanchor(anchor, now)
		if !sim.CanAfford(anchor.Amounts, cost) {
			return fmt.Errorf("insufficient resources")
		}
		anchor.Amounts = sim.Spend(anchor.Amounts, cost)
		if err := saveAnchor(ctx, tx, cityID, anchor); err != nil {
			return err
		}
		queueID = uuid.NewString()
		endsAt = now.Add(dur)
		if _, err := tx.Exec(ctx, `
			INSERT INTO train_queue(id, world_id, city_id, infantry, cavalry, archers, siege, tier, starts_at, ends_at, state)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'queued')`,
			queueID, worldID, cityID, troops.Infantry, troops.Cavalry, troops.Archers, troops.Siege, tier, now, endsAt); err != nil {
			return err
		}
		return insertEvent(ctx, tx, worldID, "train_done", queueID, endsAt)
	})
	return queueID, endsAt, err
}

// completeTrain applies a finished train_queue row (sweeper + shared path).
func completeTrain(ctx context.Context, tx Querier, s *Store, queueID string, now time.Time) error {
	var cityID string
	var inf, cav, arc, sie, worldID int
	if err := tx.QueryRow(ctx, `
		SELECT city_id, infantry, cavalry, archers, siege, world_id FROM train_queue WHERE id=$1`,
		queueID).Scan(&cityID, &inf, &cav, &arc, &sie, &worldID); err != nil {
		return err
	}
	add := func(typ string, n int) error {
		if n <= 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO troops(city_id, world_id, type, count) VALUES($1,$2,$3,$4)
			ON CONFLICT (city_id, type) DO UPDATE SET count = troops.count + $4`,
			cityID, worldID, typ, n)
		return err
	}
	for _, a := range []struct {
		typ string
		n   int
	}{{"infantry", inf}, {"cavalry", cav}, {"archers", arc}, {"siege", sie}} {
		if err := add(a.typ, a.n); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE train_queue SET state='done' WHERE id=$1`, queueID); err != nil {
		return err
	}
	cfg, _ := s.WorldConfig(ctx, worldID)
	return s.TouchCityPower(ctx, tx, cfg, cityID)
}

// CompleteBuild is the exported completion path for build_queue rows, shared
// by the sweeper and SpeedupBuild. One code path for completions.
func (s *Store) CompleteBuild(ctx context.Context, tx Querier, queueID string, now time.Time) error {
	return completeBuild(ctx, tx, s, queueID, now)
}

// CompleteTrain is the exported completion path for train_queue rows.
func (s *Store) CompleteTrain(ctx context.Context, tx Querier, queueID string, now time.Time) error {
	return completeTrain(ctx, tx, s, queueID, now)
}
