package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
)

// LaunchMarch validates and launches a march. Server-authoritative: troops
// are deducted from the city inside the transaction, duration is computed
// server-side, and arrival is scheduled as an event.
func (s *Store) LaunchMarch(ctx context.Context, playerID, originCityID string,
	troops sim.TroopCounts, tier int, tx_, ty int, purpose string, now time.Time) (marchID string, departsAt, arrivesAt time.Time, err error) {

	if troops.Total() <= 0 || troops.Total() > 500000 {
		return "", time.Time{}, time.Time{}, fmt.Errorf("invalid troop count")
	}
	if tier < 1 || tier > 3 {
		return "", time.Time{}, time.Time{}, fmt.Errorf("invalid tier")
	}
	switch purpose {
	case "attack", "gather", "reinforce", "scout":
	default:
		return "", time.Time{}, time.Time{}, fmt.Errorf("invalid purpose")
	}

	err = s.TxFunc(ctx, func(ctx context.Context, db Querier) error {
		owner, worldID, err := s.CityOwner(ctx, db, originCityID)
		if err != nil || owner != playerID {
			return fmt.Errorf("not your city")
		}
		cfg, err := s.WorldConfig(ctx, worldID)
		if err != nil {
			return err
		}
		var ox, oy int
		if err := db.QueryRow(ctx, `SELECT tile_x, tile_y FROM cities WHERE id=$1`, originCityID).Scan(&ox, &oy); err != nil {
			return err
		}
		if tx_ == ox && ty == oy {
			return fmt.Errorf("cannot march to your own tile")
		}
		// March slot cap.
		var active int
		if err := db.QueryRow(ctx,
			`SELECT count(*) FROM marches WHERE origin_city_id=$1 AND status IN ('marching','returning')`, originCityID).Scan(&active); err != nil {
			return err
		}
		if active >= cfg.MarchSlotsBase {
			return fmt.Errorf("no free march slots")
		}
		// Target legality.
		var targetCityID *string
		if purpose == "attack" {
			// Phase 1: PvP city-vs-city attacks are rejected. The schema and the
			// march code already carry target_city_id, so phase 2 needs no rewrite:
			// flip this check and resolve vs the city's garrison.
			var tc string
			if err := db.QueryRow(ctx,
				`SELECT id FROM cities WHERE world_id=$1 AND tile_x=$2 AND tile_y=$3`, worldID, tx_, ty).Scan(&tc); err == nil {
				targetCityID = &tc
				return errPvPPhase2
			}
			// Attack must target a PvE node in phase 1.
			var nodeKind string
			if err := db.QueryRow(ctx,
				`SELECT node_kind FROM tiles WHERE world_id=$1 AND x=$2 AND y=$3 AND node_kind IS NOT NULL`,
				worldID, tx_, ty).Scan(&nodeKind); err != nil {
				return fmt.Errorf("attack target must be a frost node in phase 1")
			}
		}
		if purpose == "gather" {
			var nodeKind string
			if err := db.QueryRow(ctx,
				`SELECT node_kind FROM tiles WHERE world_id=$1 AND x=$2 AND y=$3 AND node_kind IS NOT NULL AND (node_amount IS NULL OR node_amount > 0)`,
				worldID, tx_, ty).Scan(&nodeKind); err != nil {
				return fmt.Errorf("gather target must be an active resource node")
			}
		}
		if purpose == "reinforce" || purpose == "scout" {
			var tc string
			if err := db.QueryRow(ctx,
				`SELECT id FROM cities WHERE world_id=$1 AND tile_x=$2 AND tile_y=$3`, worldID, tx_, ty).Scan(&tc); err != nil {
				return fmt.Errorf("target city not found")
			}
			if purpose == "reinforce" {
				targetCityID = &tc // stored for phase-2 garrison logic; phase 1: returns home
			}
		}
		// Deduct troops from the city (they ride with the march).
		sub := func(typ string, n int64) error {
			if n <= 0 {
				return nil
			}
			tag, err := db.Exec(ctx,
				`UPDATE troops SET count = count - $3 WHERE city_id=$1 AND type=$2 AND count >= $3`,
				originCityID, typ, n)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("not enough %s", typ)
			}
			return nil
		}
		if err := sub("infantry", troops.Infantry); err != nil {
			return err
		}
		if err := sub("cavalry", troops.Cavalry); err != nil {
			return err
		}
		if err := sub("archers", troops.Archers); err != nil {
			return err
		}
		if err := sub("siege", troops.Siege); err != nil {
			return err
		}

		dur := sim.MarchDuration(cfg, troops, tier, ox, oy, tx_, ty)
		marchID = uuid.NewString()
		departsAt = now
		arrivesAt = now.Add(dur)
		if _, err := db.Exec(ctx, `
			INSERT INTO marches(id, player_id, world_id, origin_city_id, target_x, target_y,
				target_city_id, purpose, status, departs_at, arrives_at, tier)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'marching',$9,$10,$11)`,
			marchID, playerID, worldID, originCityID, tx_, ty, targetCityID, purpose, departsAt, arrivesAt, tier); err != nil {
			return err
		}
		add := func(typ string, n int64) error {
			if n <= 0 {
				return nil
			}
			_, err := db.Exec(ctx, `INSERT INTO march_troops(march_id, type, count) VALUES($1,$2,$3)`, marchID, typ, n)
			return err
		}
		for _, a := range []struct {
			typ string
			n   int64
		}{{"infantry", troops.Infantry}, {"cavalry", troops.Cavalry}, {"archers", troops.Archers}, {"siege", troops.Siege}} {
			if err := add(a.typ, a.n); err != nil {
				return err
			}
		}
		if err := insertEvent(ctx, db, worldID, "march_arrive", marchID, arrivesAt); err != nil {
			return err
		}
		return s.TouchCityPower(ctx, db, cfg, originCityID)
	})
	return marchID, departsAt, arrivesAt, err
}

// errPvPPhase2 is the frozen phase-1 rejection for city-vs-city attacks.
type pvpPhase2Error struct{}

func (pvpPhase2Error) Error() string { return "pvp_phase2" }

var errPvPPhase2 = pvpPhase2Error{}

// IsPvPPhase2 reports whether err is the phase-1 PvP rejection.
func IsPvPPhase2(err error) bool { return err == errPvPPhase2 }

// RecallMarch recalls a marching march; it turns around from its current
// position and returns home.
func (s *Store) RecallMarch(ctx context.Context, playerID, marchID string, now time.Time) error {
	return s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var worldID int
		var status, originCityID string
		var departsAt, arrivesAt time.Time
		var ox, oy, txx, tyy int
		if err := tx.QueryRow(ctx, `
			SELECT m.world_id, m.status, m.origin_city_id, m.departs_at, m.arrives_at,
			       c.tile_x, c.tile_y, m.target_x, m.target_y
			FROM marches m JOIN cities c ON c.id = m.origin_city_id
			WHERE m.id=$1 AND m.player_id=$2`, marchID, playerID).
			Scan(&worldID, &status, &originCityID, &departsAt, &arrivesAt, &ox, &oy, &txx, &tyy); err != nil {
			return fmt.Errorf("march not found")
		}
		if status != "marching" {
			return fmt.Errorf("march cannot be recalled")
		}
		// Turn around from current interpolated position.
		cx, cy, _ := sim.MarchPosition(ox, oy, txx, tyy, departsAt, arrivesAt, now)
		// Remaining leg home: reuse duration math for the remaining distance.
		troops, _, err := marchTroops(ctx, tx, marchID)
		if err != nil {
			return err
		}
		cfg, _ := s.WorldConfig(ctx, worldID)
		dur := sim.MarchDuration(cfg, troops, 1, int(cx), int(cy), ox, oy)
		returnsAt := now.Add(dur)
		if _, err := tx.Exec(ctx,
			`UPDATE marches SET status='returning', returns_at=$2 WHERE id=$1`, marchID, returnsAt); err != nil {
			return err
		}
		// Cancel the arrival event; schedule the return.
		if _, err := tx.Exec(ctx,
			`UPDATE scheduled_events SET status='done' WHERE kind='march_arrive' AND ref_id=$1 AND status='pending'`, marchID); err != nil {
			return err
		}
		return insertEvent(ctx, tx, worldID, "march_return", marchID, returnsAt)
	})
}

// ListMarches returns the player's active marches.
func (s *Store) ListMarches(ctx context.Context, playerID string) ([]March, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, player_id, world_id, origin_city_id, target_x, target_y, target_city_id,
		       purpose, status, departs_at, arrives_at, returns_at
		FROM marches WHERE player_id=$1 AND status IN ('marching','arrived','returning')
		ORDER BY arrives_at`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []March
	for rows.Next() {
		var m March
		if err := rows.Scan(&m.ID, &m.PlayerID, &m.WorldID, &m.OriginCityID, &m.TargetX, &m.TargetY,
			&m.TargetCityID, &m.Purpose, &m.Status, &m.DepartsAt, &m.ArrivesAt, &m.ReturnsAt); err != nil {
			return nil, err
		}
		t, tier, err := marchTroops(ctx, s.DB, m.ID)
		if err != nil {
			return nil, err
		}
		m.Troops = t
		m.Tier = tier
		out = append(out, m)
	}
	return out, rows.Err()
}

// marchTroops loads a march's troops and the launch-time tier (marches.tier,
// added in migration 000003).
func marchTroops(ctx context.Context, q Querier, marchID string) (sim.TroopCounts, int, error) {
	rows, err := q.Query(ctx, `SELECT type, count FROM march_troops WHERE march_id=$1`, marchID)
	if err != nil {
		return sim.TroopCounts{}, 1, err
	}
	defer rows.Close()
	var t sim.TroopCounts
	for rows.Next() {
		var typ string
		var n int
		if err := rows.Scan(&typ, &n); err != nil {
			return t, 1, err
		}
		switch typ {
		case "infantry":
			t.Infantry = int64(n)
		case "cavalry":
			t.Cavalry = int64(n)
		case "archers":
			t.Archers = int64(n)
		case "siege":
			t.Siege = int64(n)
		}
	}
	var tier int
	if err := q.QueryRow(ctx, `SELECT tier FROM marches WHERE id=$1`, marchID).Scan(&tier); err != nil {
		tier = 1
	}
	return t, tier, rows.Err()
}

// ResolveMarchArrival runs when a march reaches its target. One code path for
// the sweeper; deterministic battle via sim.ResolveBattle.
func (s *Store) ResolveMarchArrival(ctx context.Context, tx Querier, marchID string, now time.Time) error {
	var m March
	if err := tx.QueryRow(ctx, `
		SELECT id, player_id, world_id, origin_city_id, target_x, target_y, target_city_id,
		       purpose, status, departs_at, arrives_at
		FROM marches WHERE id=$1`, marchID).
		Scan(&m.ID, &m.PlayerID, &m.WorldID, &m.OriginCityID, &m.TargetX, &m.TargetY,
			&m.TargetCityID, &m.Purpose, &m.Status, &m.DepartsAt, &m.ArrivesAt); err != nil {
		return err
	}
	if m.Status != "marching" {
		return nil // already handled (recall raced the sweeper)
	}
	troops, tier, err := marchTroops(ctx, tx, marchID)
	if err != nil {
		return err
	}
	cfg, err := s.WorldConfig(ctx, m.WorldID)
	if err != nil {
		return err
	}
	switch m.Purpose {
	case "attack":
		return resolveNodeAttack(ctx, tx, s, cfg, m, troops, tier, now)
	case "gather":
		return resolveGather(ctx, tx, s, cfg, m, troops, tier, now)
	case "reinforce", "scout":
		// Phase 1: reinforce/scout visit and return (no garrison mechanics yet).
		return sendMarchHome(ctx, tx, m.WorldID, marchID, now)
	default:
		return fmt.Errorf("unknown purpose %s", m.Purpose)
	}
}

// resolveNodeAttack: PvE frost-node battle. Same resolver as PvP will use.
func resolveNodeAttack(ctx context.Context, tx Querier, s *Store, cfg sim.GameConfig,
	m March, troops sim.TroopCounts, tier int, now time.Time) error {

	var nodeKind string
	var nodeAmount int
	if err := tx.QueryRow(ctx,
		`SELECT node_kind, node_amount FROM tiles WHERE world_id=$1 AND x=$2 AND y=$3`,
		m.WorldID, m.TargetX, m.TargetY).Scan(&nodeKind, &nodeAmount); err != nil {
		// Node gone (depleted by someone else): march home empty-handed.
		return sendMarchHome(ctx, tx, m.WorldID, m.ID, now)
	}
	// Scripted garrison scales with node richness: ~1 defender per 25 resources.
	defCount := int64(nodeAmount / 25)
	if defCount < 10 {
		defCount = 10
	}
	defTroops := sim.TroopCounts{Infantry: defCount / 2, Archers: defCount / 2}
	atk := sim.Army{Troops: troops, Tier: tier, CommanderMult: 1.0, TechMult: 1.0}
	def := sim.Army{Troops: defTroops, Tier: 1, CommanderMult: 1.0, TechMult: 1.0}
	seed := int64(m.ArrivesAt.UnixNano())
	res := sim.ResolveBattle(cfg, atk, def, 1.0, sim.ResourceBundle{}, seed)

	battleID := uuid.NewString()
	logJSON, _ := json.Marshal(map[string]any{
		"attacker_power": res.AttackerPower, "defender_power": res.DefenderPower,
		"attacker_left": res.AttackerLeft, "defender_left": res.DefenderLeft,
		"plunder": res.Plunder, "node": map[string]any{"x": m.TargetX, "y": m.TargetY, "kind": nodeKind},
	})
	outcome := "defender_win"
	if res.AttackerWon {
		outcome = "attacker_win"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO battles(id, world_id, march_id, attacker_player_id, defender_node, outcome, log)
		VALUES($1,$2,$3,$4,$5,$6,$7)`,
		battleID, m.WorldID, m.ID, m.PlayerID,
		map[string]any{"x": m.TargetX, "y": m.TargetY, "kind": nodeKind},
		outcome, logJSON); err != nil {
		return err
	}
	summary := map[string]any{
		"type": "node_attack", "won": res.AttackerWon,
		"node":          map[string]any{"x": m.TargetX, "y": m.TargetY, "kind": nodeKind},
		"losses":        map[string]any{"attacker_pct": res.AttackerLossPct, "defender_pct": res.DefenderLossPct},
		"attacker_left": res.AttackerLeft, "plunder": res.Plunder,
	}
	if err := insertReport(ctx, tx, battleID, m.PlayerID, m.WorldID, summary); err != nil {
		return err
	}
	if res.AttackerWon {
		// Loot the node: convert node_amount into resources carried home.
		loot := nodeAmount
		cap := sim.CarryCapacity(cfg, res.AttackerLeft, tier)
		if int64(loot) > cap {
			loot = int(cap)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tiles SET node_amount = node_amount - $4
			 WHERE world_id=$1 AND x=$2 AND y=$3`,
			m.WorldID, m.TargetX, m.TargetY, loot); err != nil {
			return err
		}
		// If the node depleted, schedule its respawn (2h) with the tile coords
		// in the event payload.
		var remaining int
		_ = tx.QueryRow(ctx, `SELECT node_amount FROM tiles WHERE world_id=$1 AND x=$2 AND y=$3`,
			m.WorldID, m.TargetX, m.TargetY).Scan(&remaining)
		if remaining <= 0 {
			payload, _ := json.Marshal(map[string]any{"x": m.TargetX, "y": m.TargetY})
			if _, err := tx.Exec(ctx,
				`INSERT INTO scheduled_events(world_id, kind, ref_id, run_at, payload)
				 VALUES($1,'node_respawn',$2,$3,$4)`,
				m.WorldID, uuid.NewString(), now.Add(2*time.Hour), payload); err != nil {
				return err
			}
		}
		// Survivors carry loot home: stash loot on the march row via returns.
		if err := sendMarchHome(ctx, tx, m.WorldID, m.ID, now); err != nil {
			return err
		}
		grant := sim.ResourceBundle{}
		switch nodeKind {
		case "wood":
			grant.Wood = int64(loot)
		case "stone":
			grant.Stone = int64(loot)
		default:
			grant.Food = int64(loot)
		}
		// Loot is granted when the march returns (see completeMarchReturn loot
		// sidecar). Phase-1 simplification: grant immediately to the origin city,
		// clamped by caps — the march still visually returns.
		anchor, err := loadAnchor(ctx, tx, m.OriginCityID)
		if err != nil {
			return err
		}
		anchor = sim.Reanchor(anchor, now)
		anchor.Amounts = sim.Grant(anchor.Amounts, anchor.Caps, grant)
		if err := saveAnchor(ctx, tx, m.OriginCityID, anchor); err != nil {
			return err
		}
		// Survivors return to the city.
		return returnTroopsToCity(ctx, tx, m.ID, m.OriginCityID, m.WorldID, res.AttackerLeft)
	}
	// Defeat: survivors limp home, node keeps its resources.
	if err := sendMarchHome(ctx, tx, m.WorldID, m.ID, now); err != nil {
		return err
	}
	return returnTroopsToCity(ctx, tx, m.ID, m.OriginCityID, m.WorldID, res.AttackerLeft)
}

// resolveGather: gatherers harvest node resources over a fixed gather time,
// then return. Phase-1: instant harvest on arrival, capped by carry capacity.
func resolveGather(ctx context.Context, tx Querier, s *Store, cfg sim.GameConfig,
	m March, troops sim.TroopCounts, tier int, now time.Time) error {

	var nodeKind string
	var nodeAmount int
	if err := tx.QueryRow(ctx,
		`SELECT node_kind, node_amount FROM tiles WHERE world_id=$1 AND x=$2 AND y=$3`,
		m.WorldID, m.TargetX, m.TargetY).Scan(&nodeKind, &nodeAmount); err != nil || nodeAmount <= 0 {
		return sendMarchHome(ctx, tx, m.WorldID, m.ID, now)
	}
	// Gather yield: 30 minutes of gather rate, capped by node and carry.
	yield := int(float64(cfg.GatherRatePerMin) * 30)
	if yield > nodeAmount {
		yield = nodeAmount
	}
	cap := sim.CarryCapacity(cfg, troops, tier)
	if int64(yield) > cap {
		yield = int(cap)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE tiles SET node_amount = node_amount - $4
		 WHERE world_id=$1 AND x=$2 AND y=$3`,
		m.WorldID, m.TargetX, m.TargetY, yield); err != nil {
		return err
	}
	var remaining int
	_ = tx.QueryRow(ctx, `SELECT node_amount FROM tiles WHERE world_id=$1 AND x=$2 AND y=$3`,
		m.WorldID, m.TargetX, m.TargetY).Scan(&remaining)
	if remaining <= 0 {
		payload, _ := json.Marshal(map[string]any{"x": m.TargetX, "y": m.TargetY})
		if _, err := tx.Exec(ctx,
			`INSERT INTO scheduled_events(world_id, kind, ref_id, run_at, payload)
			 VALUES($1,'node_respawn',$2,$3,$4)`,
			m.WorldID, uuid.NewString(), now.Add(2*time.Hour), payload); err != nil {
			return err
		}
	}
	grant := sim.ResourceBundle{}
	switch nodeKind {
	case "wood":
		grant.Wood = int64(yield)
	case "stone":
		grant.Stone = int64(yield)
	default:
		grant.Food = int64(yield)
	}
	anchor, err := loadAnchor(ctx, tx, m.OriginCityID)
	if err != nil {
		return err
	}
	anchor = sim.Reanchor(anchor, now)
	anchor.Amounts = sim.Grant(anchor.Amounts, anchor.Caps, grant)
	if err := saveAnchor(ctx, tx, m.OriginCityID, anchor); err != nil {
		return err
	}
	summary := map[string]any{
		"type": "gather", "node": map[string]any{"x": m.TargetX, "y": m.TargetY, "kind": nodeKind},
		"gathered": grant,
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO battle_reports(id, player_id, world_id, summary) VALUES($1,$2,$3,$4)`,
		uuid.NewString(), m.PlayerID, m.WorldID, summary); err != nil {
		return err
	}
	if err := sendMarchHome(ctx, tx, m.WorldID, m.ID, now); err != nil {
		return err
	}
	return returnTroopsToCity(ctx, tx, m.ID, m.OriginCityID, m.WorldID, troops)
}

// sendMarchHome flips a march to returning with a return leg of the same
// duration, and schedules march_return.
func sendMarchHome(ctx context.Context, tx Querier, worldID int, marchID string, now time.Time) error {
	var departsAt, arrivesAt time.Time
	if err := tx.QueryRow(ctx, `SELECT departs_at, arrives_at FROM marches WHERE id=$1`, marchID).
		Scan(&departsAt, &arrivesAt); err != nil {
		return err
	}
	leg := arrivesAt.Sub(departsAt)
	if leg < 30*time.Second {
		leg = 30 * time.Second
	}
	returnsAt := now.Add(leg)
	if _, err := tx.Exec(ctx,
		`UPDATE marches SET status='returning', returns_at=$2 WHERE id=$1`, marchID, returnsAt); err != nil {
		return err
	}
	return insertEvent(ctx, tx, worldID, "march_return", marchID, returnsAt)
}

// completeMarchReturn runs when the return leg finishes: troops rejoin the city.
func (s *Store) CompleteMarchReturn(ctx context.Context, tx Querier, marchID string, now time.Time) error {
	var originCityID string
	var worldID int
	var status string
	if err := tx.QueryRow(ctx,
		`SELECT origin_city_id, world_id, status FROM marches WHERE id=$1`, marchID).
		Scan(&originCityID, &worldID, &status); err != nil {
		return err
	}
	if status != "returning" {
		return nil
	}
	troops, _, err := marchTroops(ctx, tx, marchID)
	if err != nil {
		return err
	}
	if err := returnTroopsToCity(ctx, tx, marchID, originCityID, worldID, troops); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE marches SET status='done' WHERE id=$1`, marchID)
	return err
}

// returnTroopsToCity moves an army's troops back into a city and clears the
// march_troops rows, so the later march_return event cannot add them twice.
// Phase-1 simplification: troops are accounted at arrival; the return leg is
// a visual timer.
func returnTroopsToCity(ctx context.Context, tx Querier, marchID, cityID string, worldID int, t sim.TroopCounts) error {
	add := func(typ string, n int64) error {
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
		n   int64
	}{{"infantry", t.Infantry}, {"cavalry", t.Cavalry}, {"archers", t.Archers}, {"siege", t.Siege}} {
		if err := add(a.typ, a.n); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM march_troops WHERE march_id=$1`, marchID)
	return err
}

// RespawnNode refills a depleted frost node.
func (s *Store) RespawnNode(ctx context.Context, tx Querier, worldID, x, y int, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE tiles SET node_amount = 2000 + (random()*3000)::int, node_respawn_at = NULL
		WHERE world_id=$1 AND x=$2 AND y=$3 AND node_kind IS NOT NULL`, worldID, x, y)
	return err
}
