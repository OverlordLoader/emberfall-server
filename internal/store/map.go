package store

import (
	"context"
	"time"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
)

// MapBBox is the response for GET /v1/map.
type MapBBox struct {
	Tiles   []Tile     `json:"tiles"`
	Cities  []MapCity  `json:"cities"`
	Marches []MapMarch `json:"marches"`
}

// GetMapBBox returns tiles + cities + marches in a bbox. Visibility rule:
// city/march details (owner name, troop counts, power) are only included when
// within scout range of one of the player's cities or active marches;
// otherwise the client gets terrain + "unknown forces".
func (s *Store) GetMapBBox(ctx context.Context, playerID string, worldID int,
	x1, y1, x2, y2 int, now time.Time) (MapBBox, error) {

	var out MapBBox
	// Clamp bbox size to keep the query bounded.
	if x2-x1 > 64 {
		x2 = x1 + 64
	}
	if y2-y1 > 64 {
		y2 = y1 + 64
	}
	cfg, err := s.WorldConfig(ctx, worldID)
	if err != nil {
		return out, err
	}
	// Player's scout anchors: their cities (with watchtower levels) + marches.
	type anchor struct{ x, y, rng int }
	var anchors []anchor
	crows, err := s.DB.Query(ctx, `
		SELECT c.tile_x, c.tile_y, COALESCE(b.level, 0)
		FROM cities c LEFT JOIN buildings b ON b.city_id=c.id AND b.type='watchtower'
		WHERE c.player_id=$1 AND c.world_id=$2`, playerID, worldID)
	if err != nil {
		return out, err
	}
	for crows.Next() {
		var a anchor
		var wt int
		if err := crows.Scan(&a.x, &a.y, &wt); err != nil {
			crows.Close()
			return out, err
		}
		a.rng = sim.ScoutRange(cfg, wt)
		anchors = append(anchors, a)
	}
	crows.Close()
	mrows, err := s.DB.Query(ctx, `
		SELECT target_x, target_y FROM marches
		WHERE player_id=$1 AND world_id=$2 AND status='marching'`, playerID, worldID)
	if err != nil {
		return out, err
	}
	for mrows.Next() {
		var a anchor
		if err := mrows.Scan(&a.x, &a.y); err != nil {
			mrows.Close()
			return out, err
		}
		a.rng = cfg.ScoutRangeBase // marches scout a little around their target
		anchors = append(anchors, a)
	}
	mrows.Close()
	inRange := func(x, y int) bool {
		for _, a := range anchors {
			dx := x - a.x
			dy := y - a.y
			if dx*dx+dy*dy <= a.rng*a.rng {
				return true
			}
		}
		return false
	}

	// Tiles that exist as rows (nodes, owned tiles). Missing tiles are plain.
	trows, err := s.DB.Query(ctx, `
		SELECT x, y, terrain, owner_city_id, node_kind, node_amount FROM tiles
		WHERE world_id=$1 AND x BETWEEN $2 AND $3 AND y BETWEEN $4 AND $5`,
		worldID, x1, x2, y1, y2)
	if err != nil {
		return out, err
	}
	for trows.Next() {
		var t Tile
		if err := trows.Scan(&t.X, &t.Y, &t.Terrain, &t.OwnerCityID, &t.NodeKind, &t.NodeAmount); err != nil {
			trows.Close()
			return out, err
		}
		out.Tiles = append(out.Tiles, t)
	}
	trows.Close()

	// Cities in bbox.
	cityRows, err := s.DB.Query(ctx, `
		SELECT c.id, c.player_id, p.display_name, c.name, c.tile_x, c.tile_y, c.level, c.power,
		       al.tag
		FROM cities c JOIN players p ON p.id=c.player_id
		LEFT JOIN alliance_members am ON am.player_id=c.player_id AND am.world_id=c.world_id
		LEFT JOIN alliances al ON al.id=am.alliance_id
		WHERE c.world_id=$1 AND c.tile_x BETWEEN $2 AND $3 AND c.tile_y BETWEEN $4 AND $5`,
		worldID, x1, x2, y1, y2)
	if err != nil {
		return out, err
	}
	for cityRows.Next() {
		var mc MapCity
		var pname string
		if err := cityRows.Scan(&mc.CityID, &mc.PlayerID, &pname, &mc.Name, &mc.X, &mc.Y,
			&mc.Level, &mc.Power, &mc.AllianceTag); err != nil {
			cityRows.Close()
			return out, err
		}
		if !inRange(mc.X, mc.Y) {
			// Unknown forces: strip details but keep presence.
			mc.PlayerName = ""
			mc.Power = nil
			mc.AllianceTag = nil
			pname = ""
		} else {
			mc.PlayerName = pname
		}
		_ = pname
		out.Cities = append(out.Cities, mc)
	}
	cityRows.Close()

	// Marches in bbox (positions interpolated server-side).
	mmrows, err := s.DB.Query(ctx, `
		SELECT id, player_id, origin_city_id, target_x, target_y, purpose, status, departs_at, arrives_at
		FROM marches WHERE world_id=$1 AND status='marching'`, worldID)
	if err != nil {
		return out, err
	}
	for mmrows.Next() {
		var id, pid, ocid, purpose, status string
		var txx, tyy int
		var dep, arr time.Time
		if err := mmrows.Scan(&id, &pid, &ocid, &txx, &tyy, &purpose, &status, &dep, &arr); err != nil {
			mmrows.Close()
			return out, err
		}
		var ox, oy int
		if err := s.DB.QueryRow(ctx, `SELECT tile_x, tile_y FROM cities WHERE id=$1`, ocid).Scan(&ox, &oy); err != nil {
			continue
		}
		x, y, prog := sim.MarchPosition(ox, oy, txx, tyy, dep, arr, now)
		if int(x) < x1 || int(x) > x2 || int(y) < y1 || int(y) > y2 {
			continue
		}
		mm := MapMarch{MarchID: id, PlayerID: pid, X: x, Y: y, Progress: prog, Purpose: purpose}
		if inRange(int(x), int(y)) {
			t, _, err := marchTroops(ctx, s.DB, id)
			if err == nil {
				mm.Troops = &t
			}
		}
		out.Marches = append(out.Marches, mm)
	}
	mmrows.Close()
	if out.Tiles == nil {
		out.Tiles = []Tile{}
	}
	if out.Cities == nil {
		out.Cities = []MapCity{}
	}
	if out.Marches == nil {
		out.Marches = []MapMarch{}
	}
	return out, mmrows.Err()
}
