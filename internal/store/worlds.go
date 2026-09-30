package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
)

// ListWorlds returns worlds with live player counts.
func (s *Store) ListWorlds(ctx context.Context) ([]World, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT w.id, w.name, w.status, w.created_at,
		       (SELECT count(DISTINCT player_id) FROM cities c WHERE c.world_id=w.id) AS players
		FROM worlds w ORDER BY w.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []World
	for rows.Next() {
		var w World
		if err := rows.Scan(&w.ID, &w.Name, &w.Status, &w.CreatedAt, &w.Players); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// JoinWorld is idempotent per player+world: if the player already has a city
// there, its id is returned and nothing is created.
func (s *Store) JoinWorld(ctx context.Context, worldID int, playerID string) (cityID string, created bool, err error) {
	err = s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		// Idempotency: existing city wins.
		if err := tx.QueryRow(ctx,
			`SELECT id FROM cities WHERE world_id=$1 AND player_id=$2 ORDER BY created_at LIMIT 1`,
			worldID, playerID).Scan(&cityID); err == nil {
			return nil
		}
		var status string
		var size int
		if err := tx.QueryRow(ctx, `SELECT status, size FROM worlds WHERE id=$1`, worldID).
			Scan(&status, &size); err != nil {
			return fmt.Errorf("world not found")
		}
		if status == "closed" {
			return fmt.Errorf("world closed")
		}
		x, y, err := findFreeTile(ctx, tx, worldID, size)
		if err != nil {
			return err
		}
		cityID = uuid.NewString()
		name := "Emberhold"
		if _, err := tx.Exec(ctx,
			`INSERT INTO cities(id, player_id, world_id, name, tile_x, tile_y) VALUES($1,$2,$3,$4,$5,$6)`,
			cityID, playerID, worldID, name, x, y); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO tiles(world_id, x, y, terrain, owner_city_id) VALUES($1,$2,$3,'plain',$4)`,
			worldID, x, y, cityID); err != nil {
			return err
		}
		cfg, _ := s.WorldConfig(ctx, worldID)
		r := cfg.CityFoundResources
		c := cfg.CityFoundCaps
		now := time.Now()
		if _, err := tx.Exec(ctx, `
			INSERT INTO city_resources(city_id, wood_amt, wood_cap, stone_amt, stone_cap,
				food_amt, food_cap, gold_amt, gold_cap, updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			cityID, r.Wood, c.Wood, r.Stone, c.Stone, r.Food, c.Food, r.Gold, c.Gold, now); err != nil {
			return err
		}
		// Starter buildings: town hall + one of each resource building.
		for i, b := range []struct {
			typ string
			lvl int
		}{{"town_hall", 1}, {"farm", 1}, {"lumber_mill", 1}, {"quarry", 1}, {"gold_mine", 1}} {
			if _, err := tx.Exec(ctx,
				`INSERT INTO buildings(id, city_id, world_id, type, level, slot, state)
				 VALUES($1,$2,$3,$4,$5,$6,'active')`,
				uuid.NewString(), cityID, worldID, b.typ, b.lvl, i); err != nil {
				return err
			}
		}
		// Starter garrison so a fresh city is not defenseless.
		for _, t := range []string{"infantry", "cavalry", "archers", "siege"} {
			if _, err := tx.Exec(ctx,
				`INSERT INTO troops(city_id, world_id, type, count) VALUES($1,$2,$3,$4)`,
				cityID, worldID, t, 20); err != nil {
				return err
			}
		}
		if err := recomputeRates(ctx, tx, s, worldID, cityID, now); err != nil {
			return err
		}
		// Welcome starter items: a few speedups so the first session feels good.
		if _, err := tx.Exec(ctx,
			`INSERT INTO inventory(player_id, world_id, item_key, qty)
			 VALUES($1,$2,'speedup_small',2),($1,$2,'summon_ticket',1)
			 ON CONFLICT (player_id, item_key) DO UPDATE SET qty = inventory.qty + EXCLUDED.qty`,
			playerID, worldID); err != nil {
			return err
		}
		created = true
		return nil
	})
	return cityID, created, err
}

// findFreeTile picks a random tile with no city within minDist tiles.
func findFreeTile(ctx context.Context, tx Querier, worldID, size int) (int, int, error) {
	const minDist = 12
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for tries := 0; tries < 200; tries++ {
		x := rng.Intn(size)
		y := rng.Intn(size)
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM cities
			WHERE world_id=$1 AND abs(tile_x-$2) < $4 AND abs(tile_y-$3) < $4`,
			worldID, x, y, minDist).Scan(&n); err != nil {
			return 0, 0, err
		}
		if n == 0 {
			return x, y, nil
		}
	}
	return 0, 0, fmt.Errorf("no free tile found")
}

// SeedFrostNodes scatters PvE resource nodes across a new world.
// Idempotent-ish: skips tiles that already have a node or owner.
func (s *Store) SeedFrostNodes(ctx context.Context, worldID int, count int) error {
	var size int
	if err := s.DB.QueryRow(ctx, `SELECT size FROM worlds WHERE id=$1`, worldID).Scan(&size); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `
		INSERT INTO tiles(world_id, x, y, terrain, node_kind, node_amount)
		SELECT $1, (random()*$2)::int, (random()*$2)::int, 'node',
		       (ARRAY['wood','stone','food'])[floor(random()*3)+1],
		       2000 + (random()*3000)::int
		FROM generate_series(1, $3)
		ON CONFLICT (world_id, x, y) DO NOTHING`, worldID, size, count)
	return err
}

// recomputeRates derives per-second rates + caps from building levels and
// writes them to city_resources (keeping the lazy anchor's amounts).
func recomputeRates(ctx context.Context, tx Querier, s *Store, worldID int, cityID string, now time.Time) error {
	cfg, err := s.WorldConfig(ctx, worldID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT type, level FROM buildings WHERE city_id=$1`, cityID)
	if err != nil {
		return err
	}
	levels := map[string]int{}
	for rows.Next() {
		var typ string
		var lvl int
		if err := rows.Scan(&typ, &lvl); err != nil {
			rows.Close()
			return err
		}
		levels[typ] = lvl
	}
	rows.Close()
	rates, caps := sim.RatesForCity(cfg, levels)
	_, err = tx.Exec(ctx, `
		UPDATE city_resources SET wood_rate=$2, stone_rate=$3, food_rate=$4, gold_rate=$5,
			wood_cap=$6, stone_cap=$7, food_cap=$8, gold_cap=$9, updated_at=$10
		WHERE city_id=$1`,
		cityID, rates.Wood, rates.Stone, rates.Food, rates.Gold,
		caps.Wood, caps.Stone, caps.Food, caps.Gold, now)
	return err
}

// loadAnchor reads the lazy resource anchor for a city.
func loadAnchor(ctx context.Context, q Querier, cityID string) (sim.ResourceAnchor, error) {
	var a sim.ResourceAnchor
	err := q.QueryRow(ctx, `
		SELECT wood_amt, wood_rate, wood_cap, stone_amt, stone_rate, stone_cap,
		       food_amt, food_rate, food_cap, gold_amt, gold_rate, gold_cap, updated_at
		FROM city_resources WHERE city_id=$1`, cityID).Scan(
		&a.Amounts.Wood, &a.Rates.Wood, &a.Caps.Wood,
		&a.Amounts.Stone, &a.Rates.Stone, &a.Caps.Stone,
		&a.Amounts.Food, &a.Rates.Food, &a.Caps.Food,
		&a.Amounts.Gold, &a.Rates.Gold, &a.Caps.Gold,
		&a.Updated)
	return a, err
}

// saveAnchor writes the anchor back (amounts + updated_at).
func saveAnchor(ctx context.Context, q Querier, cityID string, a sim.ResourceAnchor) error {
	_, err := q.Exec(ctx, `
		UPDATE city_resources SET wood_amt=$2, stone_amt=$3, food_amt=$4, gold_amt=$5, updated_at=$6
		WHERE city_id=$1`,
		cityID, a.Amounts.Wood, a.Amounts.Stone, a.Amounts.Food, a.Amounts.Gold, a.Updated)
	return err
}

// configJSON marshals a GameConfig for API output.
func configJSON(cfg sim.GameConfig) []byte {
	b, _ := json.Marshal(cfg)
	return b
}
