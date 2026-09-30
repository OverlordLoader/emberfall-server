package store

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// insertReport writes a battle report for a player.
func insertReport(ctx context.Context, q Querier, battleID, playerID string, worldID int, summary map[string]any) error {
	sumJSON, _ := json.Marshal(summary)
	var err error
	if battleID == "" {
		_, err = q.Exec(ctx,
			`INSERT INTO battle_reports(id, player_id, world_id, summary) VALUES($1,$2,$3,$4)`,
			uuid.NewString(), playerID, worldID, sumJSON)
	} else {
		_, err = q.Exec(ctx,
			`INSERT INTO battle_reports(id, battle_id, player_id, world_id, summary) VALUES($1,$2,$3,$4,$5)`,
			uuid.NewString(), battleID, playerID, worldID, sumJSON)
	}
	return err
}

// ListReports returns a player's battle reports, newest first, cursor-paginated.
func (s *Store) ListReports(ctx context.Context, playerID string, cursor string, limit int) ([]BattleReport, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	q := `SELECT id, battle_id, summary, created_at FROM battle_reports
	      WHERE player_id=$1 AND ($2='' OR created_at < $2::timestamptz)
	      ORDER BY created_at DESC LIMIT $3`
	r, err := s.DB.Query(ctx, q, playerID, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	var out []BattleReport
	for r.Next() {
		var br BattleReport
		var battleID *string
		var sumJSON []byte
		if err := r.Scan(&br.ID, &battleID, &sumJSON, &br.CreatedAt); err != nil {
			return nil, "", err
		}
		if battleID != nil {
			br.BattleID = *battleID
		}
		_ = json.Unmarshal(sumJSON, &br.Summary)
		out = append(out, br)
	}
	if err := r.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return out, next, nil
}
