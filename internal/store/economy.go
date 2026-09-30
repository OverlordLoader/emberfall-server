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

// ---- items & inventory ----

// ListInventory returns the player's items with catalog metadata.
func (s *Store) ListInventory(ctx context.Context, playerID string) ([]InventoryItem, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT i.key, i.kind, inv.qty, i.data
		FROM inventory inv JOIN items i ON i.key=inv.item_key
		WHERE inv.player_id=$1 AND inv.qty > 0 ORDER BY i.key`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InventoryItem
	for rows.Next() {
		var it InventoryItem
		var data []byte
		if err := rows.Scan(&it.Key, &it.Kind, &it.Qty, &data); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(data, &it.Data)
		out = append(out, it)
	}
	return out, rows.Err()
}

// GrantItems adds items to a player's inventory.
func (s *Store) GrantItems(ctx context.Context, q Querier, playerID string, worldID int, items map[string]int) error {
	for key, qty := range items {
		if qty <= 0 {
			continue
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO inventory(player_id, world_id, item_key, qty) VALUES($1,$2,$3,$4)
			ON CONFLICT (player_id, item_key) DO UPDATE SET qty = inventory.qty + $4`,
			playerID, worldID, key, qty); err != nil {
			return err
		}
	}
	return nil
}

// consumeItem deducts qty of an item; returns the item's speedup seconds.
// Used by SpeedupBuild.
func consumeItem(ctx context.Context, q Querier, playerID string, worldID int, itemKey string, qty int) (int64, error) {
	tag, err := q.Exec(ctx,
		`UPDATE inventory SET qty = qty - $3 WHERE player_id=$1 AND item_key=$2 AND qty >= $3`,
		playerID, itemKey, qty)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, fmt.Errorf("not enough %s", itemKey)
	}
	return itemSpeedupSeconds(ctx, q, itemKey)
}

func itemSpeedupSeconds(ctx context.Context, q Querier, itemKey string) (int64, error) {
	var data []byte
	if err := q.QueryRow(ctx, `SELECT data FROM items WHERE key=$1`, itemKey).Scan(&data); err != nil {
		return 0, fmt.Errorf("unknown item")
	}
	var d struct {
		Seconds int64 `json:"seconds"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Seconds, nil
}

// UseItem applies an item's effect. Phase 1 supported kinds:
// - speedup_* : handled via build speedup endpoint (this path grants nothing)
// - shield_8h : stubbed — recorded but shields are phase 2 (clear error)
// - summon_ticket : consumed by the summon endpoint instead
// - chest_common : grants a small resource bundle (fun, generous)
func (s *Store) UseItem(ctx context.Context, playerID, itemKey string, qty int, now time.Time) (map[string]any, error) {
	if qty <= 0 {
		qty = 1
	}
	result := map[string]any{}
	err := s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var worldID int
		var cityID string
		if err := tx.QueryRow(ctx,
			`SELECT world_id, id FROM cities WHERE player_id=$1 ORDER BY created_at LIMIT 1`,
			playerID).Scan(&worldID, &cityID); err != nil {
			return fmt.Errorf("join a world first")
		}
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM items WHERE key=$1`, itemKey).Scan(&kind); err != nil {
			return fmt.Errorf("unknown item")
		}
		switch kind {
		case "chest_common":
			if _, err := consumeItem(ctx, tx, playerID, worldID, itemKey, qty); err != nil {
				return err
			}
			grant := sim.ResourceBundle{Wood: 300 * int64(qty), Stone: 300 * int64(qty), Food: 300 * int64(qty), Gold: 100 * int64(qty)}
			anchor, err := loadAnchor(ctx, tx, cityID)
			if err != nil {
				return err
			}
			anchor = sim.Reanchor(anchor, now)
			anchor.Amounts = sim.Grant(anchor.Amounts, anchor.Caps, grant)
			if err := saveAnchor(ctx, tx, cityID, anchor); err != nil {
				return err
			}
			result["granted"] = grant
		case "shield":
			return fmt.Errorf("shields_activate_in_phase_2")
		case "speedup":
			return fmt.Errorf("use speedup via POST /v1/build-queue/{id}/speedup")
		case "summon_ticket":
			return fmt.Errorf("use tickets via POST /v1/commanders/summon")
		default:
			return fmt.Errorf("item has no use action")
		}
		return nil
	})
	return result, err
}

// ---- commanders & gacha ----

// ListCommanders returns a player's commanders.
func (s *Store) ListCommanders(ctx context.Context, playerID string) ([]Commander, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, key, name, rarity, level, xp, stars, created_at
		FROM commanders WHERE player_id=$1 ORDER BY
		  CASE rarity WHEN 'epic' THEN 0 WHEN 'rare' THEN 1 ELSE 2 END, created_at`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Commander
	for rows.Next() {
		var c Commander
		if err := rows.Scan(&c.ID, &c.Key, &c.Name, &c.Rarity, &c.Level, &c.XP, &c.Stars, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SummonCommanders performs count server-side gacha pulls. Pity is stored per
// player and survives across calls. Tickets are consumed first; without a
// ticket the pull is rejected (IAP for pulls is phase 2).
func (s *Store) SummonCommanders(ctx context.Context, playerID string, count int) ([]sim.SummonResult, error) {
	if count < 1 || count > 10 {
		return nil, fmt.Errorf("count must be 1-10")
	}
	var results []sim.SummonResult
	err := s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var worldID int
		if err := tx.QueryRow(ctx,
			`SELECT world_id FROM cities WHERE player_id=$1 ORDER BY created_at LIMIT 1`, playerID).Scan(&worldID); err != nil {
			return fmt.Errorf("join a world first")
		}
		// Tickets first.
		tag, err := tx.Exec(ctx,
			`UPDATE inventory SET qty = qty - $3 WHERE player_id=$1 AND item_key='summon_ticket' AND qty >= $3`,
			playerID, count)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not enough summon tickets")
		}
		cfg, err := s.WorldConfig(ctx, worldID)
		if err != nil {
			return err
		}
		var pity, total int
		if err := tx.QueryRow(ctx,
			`SELECT pity, total_summons FROM player_gacha WHERE player_id=$1`, playerID).Scan(&pity, &total); err != nil {
			pity, total = 0, 0
		}
		// Seeded RNG: seed mixes server secret material with pull count so
		// results are unpredictable to the client but reproducible for audit.
		rng := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(total*7919+13)))
		for i := 0; i < count; i++ {
			res, np := sim.Summon(cfg, rng, pity)
			pity = np
			results = append(results, res)
			if _, err := tx.Exec(ctx, `
				INSERT INTO commanders(id, player_id, world_id, key, name, rarity)
				VALUES($1,$2,$3,$4,$5,$6)`,
				uuid.NewString(), playerID, worldID, res.Key, res.Name, res.Rarity); err != nil {
				return err
			}
		}
		total += count
		_, err = tx.Exec(ctx, `
			INSERT INTO player_gacha(player_id, pity, total_summons) VALUES($1,$2,$3)
			ON CONFLICT (player_id) DO UPDATE SET pity=$2, total_summons=$3, updated_at=now()`,
			playerID, pity, total)
		return err
	})
	return results, err
}

// grantEpicCommanders inserts count guaranteed-epic commanders, honoring the
// pity convention: an epic pull resets pity to 0 (see sim.Summon). Used by
// IAP fulfillment for products that include guaranteed epics.
func (s *Store) grantEpicCommanders(ctx context.Context, tx Querier, playerID string, worldID int, count int) error {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < count; i++ {
		res := sim.SummonEpic(rng)
		if _, err := tx.Exec(ctx, `
			INSERT INTO commanders(id, player_id, world_id, key, name, rarity)
			VALUES($1,$2,$3,$4,$5,$6)`,
			uuid.NewString(), playerID, worldID, res.Key, res.Name, res.Rarity); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO player_gacha(player_id, pity, total_summons) VALUES($1, 0, $2)
		ON CONFLICT (player_id) DO UPDATE SET pity=0, total_summons=player_gacha.total_summons+$2, updated_at=now()`,
		playerID, count)
	return err
}

// ---- quests ----

// QuestStatuses returns today's quests with the player's progress.
func (s *Store) QuestStatuses(ctx context.Context, playerID string, day time.Time) ([]QuestStatus, error) {
	var out []QuestStatus
	for _, q := range sim.DailyQuests() {
		st := QuestStatus{
			Key: q.Key, Description: q.Description, Target: q.Target, Reward: q.Reward,
		}
		_ = s.DB.QueryRow(ctx,
			`SELECT progress, claimed FROM quest_progress WHERE player_id=$1 AND quest_key=$2 AND day=$3`,
			playerID, q.Key, day.Format("2006-01-02")).Scan(&st.Progress, &st.Claimed)
		out = append(out, st)
	}
	return out, nil
}

// BumpQuestProgress increments progress for quest keys (called by game actions).
func (s *Store) BumpQuestProgress(ctx context.Context, q Querier, playerID string, worldID int, keys []string, day time.Time, amount int) {
	for _, key := range keys {
		_, _ = q.Exec(ctx, `
			INSERT INTO quest_progress(player_id, world_id, quest_key, day, progress)
			VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (player_id, quest_key, day) DO UPDATE SET progress = quest_progress.progress + $5`,
			playerID, worldID, key, day.Format("2006-01-02"), amount)
	}
}

// ClaimQuest grants the quest reward once the target is met.
func (s *Store) ClaimQuest(ctx context.Context, playerID, questKey string, now time.Time) error {
	return s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var def *sim.DailyQuest
		for _, q := range sim.DailyQuests() {
			if q.Key == questKey {
				d := q
				def = &d
			}
		}
		if def == nil {
			return fmt.Errorf("unknown quest")
		}
		day := now.Format("2006-01-02")
		var progress int
		var claimed bool
		var worldID int
		if err := tx.QueryRow(ctx,
			`SELECT progress, claimed, world_id FROM quest_progress WHERE player_id=$1 AND quest_key=$2 AND day=$3`,
			playerID, questKey, day).Scan(&progress, &claimed, &worldID); err != nil {
			return fmt.Errorf("quest not started")
		}
		if claimed {
			return fmt.Errorf("already claimed")
		}
		if progress < def.Target {
			return fmt.Errorf("quest not complete")
		}
		var cityID string
		if err := tx.QueryRow(ctx,
			`SELECT id FROM cities WHERE player_id=$1 AND world_id=$2 ORDER BY created_at LIMIT 1`,
			playerID, worldID).Scan(&cityID); err != nil {
			return fmt.Errorf("no city")
		}
		anchor, err := loadAnchor(ctx, tx, cityID)
		if err != nil {
			return err
		}
		anchor = sim.Reanchor(anchor, now)
		anchor.Amounts = sim.Grant(anchor.Amounts, anchor.Caps, def.Reward)
		if err := saveAnchor(ctx, tx, cityID, anchor); err != nil {
			return err
		}
		if err := s.GrantItems(ctx, tx, playerID, worldID, def.RewardItems); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE quest_progress SET claimed=true WHERE player_id=$1 AND quest_key=$2 AND day=$3`,
			playerID, questKey, day)
		return err
	})
}

// ---- welcome track ----

// WelcomeStatus returns the 7-day track with claimed flags.
func (s *Store) WelcomeStatus(ctx context.Context, playerID string) ([]map[string]any, error) {
	claimed := map[int]bool{}
	rows, err := s.DB.Query(ctx, `SELECT day FROM welcome_claims WHERE player_id=$1`, playerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d int
		_ = rows.Scan(&d)
		claimed[d] = true
	}
	rows.Close()
	track := sim.WelcomeTrack()
	var out []map[string]any
	for i, q := range track {
		out = append(out, map[string]any{
			"day": i + 1, "description": q.Description, "reward": q.Reward,
			"reward_items": q.RewardItems, "claimed": claimed[i+1],
		})
	}
	return out, rows.Err()
}

// ClaimWelcome grants a welcome-track day. Days are claimed in order; missed
// days can be claimed late (no punishment mechanics).
func (s *Store) ClaimWelcome(ctx context.Context, playerID string, day int, now time.Time) error {
	if day < 1 || day > 7 {
		return fmt.Errorf("day must be 1-7")
	}
	return s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM welcome_claims WHERE player_id=$1`, playerID).Scan(&n); err != nil {
			return err
		}
		if day != n+1 {
			return fmt.Errorf("claim days in order (next: %d)", n+1)
		}
		track := sim.WelcomeTrack()
		q := track[day-1]
		var worldID int
		var cityID string
		if err := tx.QueryRow(ctx,
			`SELECT world_id, id FROM cities WHERE player_id=$1 ORDER BY created_at LIMIT 1`,
			playerID).Scan(&worldID, &cityID); err != nil {
			return fmt.Errorf("join a world first")
		}
		anchor, err := loadAnchor(ctx, tx, cityID)
		if err != nil {
			return err
		}
		anchor = sim.Reanchor(anchor, now)
		anchor.Amounts = sim.Grant(anchor.Amounts, anchor.Caps, q.Reward)
		if err := saveAnchor(ctx, tx, cityID, anchor); err != nil {
			return err
		}
		if err := s.GrantItems(ctx, tx, playerID, worldID, q.RewardItems); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO welcome_claims(player_id, world_id, day) VALUES($1,$2,$3)`,
			playerID, worldID, day)
		return err
	})
}

// ---- idempotency (Postgres source of truth; Redis is the fast path) ----

// IdemGetPG returns a stored idempotent response, if any.
func (s *Store) IdemGetPG(ctx context.Context, playerID, key string) ([]byte, bool) {
	var raw []byte
	if err := s.DB.QueryRow(ctx,
		`SELECT response FROM idempotency_keys WHERE player_id=$1 AND key=$2`, playerID, key).Scan(&raw); err != nil {
		return nil, false
	}
	return raw, true
}

// IdemSetPG stores an idempotent response.
func (s *Store) IdemSetPG(ctx context.Context, q Querier, playerID, key string, response []byte) error {
	_, err := q.Exec(ctx, `
		INSERT INTO idempotency_keys(player_id, key, response) VALUES($1,$2,$3)
		ON CONFLICT (player_id, key) DO NOTHING`, playerID, key, response)
	return err
}

// ---- sync ----

// SyncDeltas returns everything that changed since `since` for a player:
// city snapshots, active marches, new reports, inventory.
func (s *Store) SyncDeltas(ctx context.Context, playerID string, since time.Time, now time.Time) (map[string]any, error) {
	out := map[string]any{}
	// Cities owned by the player.
	rows, err := s.DB.Query(ctx, `SELECT id FROM cities WHERE player_id=$1`, playerID)
	if err != nil {
		return nil, err
	}
	var cityIDs []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		cityIDs = append(cityIDs, id)
	}
	rows.Close()
	var cities []CityFull
	for _, id := range cityIDs {
		cf, err := s.GetCityFull(ctx, id, now)
		if err == nil {
			cities = append(cities, cf)
		}
	}
	out["cities"] = cities
	marches, err := s.ListMarches(ctx, playerID)
	if err != nil {
		return nil, err
	}
	out["marches"] = marches
	reports, _, err := s.ListReports(ctx, playerID, since.Format("2006-01-02T15:04:05.999999999Z07:00"), 50)
	if err != nil {
		return nil, err
	}
	out["reports"] = reports
	inv, err := s.ListInventory(ctx, playerID)
	if err != nil {
		return nil, err
	}
	out["inventory"] = inv
	out["server_time"] = now.UTC()
	return out, nil
}
