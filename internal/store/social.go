package store

import (
	"context"
	"fmt"
	"strings"
)

// CreateAlliance creates an alliance; the creator becomes leader.
func (s *Store) CreateAlliance(ctx context.Context, worldID int, playerID, name, tag string) (int, error) {
	name = strings.TrimSpace(name)
	tag = strings.TrimSpace(tag)
	if len(name) < 3 || len(name) > 40 {
		return 0, fmt.Errorf("alliance name must be 3-40 chars")
	}
	if len(tag) < 2 || len(tag) > 5 {
		return 0, fmt.Errorf("alliance tag must be 2-5 chars")
	}
	var id int
	err := s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		// One alliance membership per world.
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM alliance_members WHERE player_id=$1 AND world_id=$2`, playerID, worldID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("already in an alliance in this world")
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO alliances(world_id, name, tag) VALUES($1,$2,$3) RETURNING id`,
			worldID, name, tag).Scan(&id); err != nil {
			return fmt.Errorf("alliance tag taken")
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO alliance_members(alliance_id, player_id, world_id, role) VALUES($1,$2,$3,'leader')`,
			id, playerID, worldID)
		return err
	})
	return id, err
}

// JoinAlliance adds a player as member.
func (s *Store) JoinAlliance(ctx context.Context, allianceID int, playerID string) error {
	return s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var worldID int
		if err := tx.QueryRow(ctx, `SELECT world_id FROM alliances WHERE id=$1`, allianceID).Scan(&worldID); err != nil {
			return fmt.Errorf("alliance not found")
		}
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM alliance_members WHERE player_id=$1 AND world_id=$2`, playerID, worldID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("already in an alliance in this world")
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO alliance_members(alliance_id, player_id, world_id, role) VALUES($1,$2,$3,'member')`,
			allianceID, playerID, worldID)
		return err
	})
}

// LeaveAlliance removes a player. If the leader leaves, leadership passes to
// the oldest officer/member; a lone leader disbands the alliance.
func (s *Store) LeaveAlliance(ctx context.Context, allianceID int, playerID string) error {
	return s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var role string
		if err := tx.QueryRow(ctx,
			`SELECT role FROM alliance_members WHERE alliance_id=$1 AND player_id=$2`, allianceID, playerID).Scan(&role); err != nil {
			return fmt.Errorf("not a member")
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM alliance_members WHERE alliance_id=$1 AND player_id=$2`, allianceID, playerID); err != nil {
			return err
		}
		if role == "leader" {
			var next string
			err := tx.QueryRow(ctx, `
				SELECT player_id FROM alliance_members WHERE alliance_id=$1
				ORDER BY CASE role WHEN 'officer' THEN 0 ELSE 1 END, joined_at LIMIT 1`, allianceID).Scan(&next)
			if err != nil {
				// Nobody left: disband.
				_, err := tx.Exec(ctx, `DELETE FROM alliances WHERE id=$1`, allianceID)
				return err
			}
			_, err = tx.Exec(ctx,
				`UPDATE alliance_members SET role='leader' WHERE alliance_id=$1 AND player_id=$2`, allianceID, next)
			return err
		}
		return nil
	})
}

// GetAlliance returns an alliance with member count.
func (s *Store) GetAlliance(ctx context.Context, allianceID int) (Alliance, error) {
	var a Alliance
	err := s.DB.QueryRow(ctx, `
		SELECT al.id, al.world_id, al.name, al.tag, al.level, al.created_at,
		       (SELECT count(*) FROM alliance_members m WHERE m.alliance_id=al.id)
		FROM alliances al WHERE al.id=$1`, allianceID).
		Scan(&a.ID, &a.WorldID, &a.Name, &a.Tag, &a.Level, &a.CreatedAt, &a.Members)
	return a, err
}

// PlayerAlliance returns the player's alliance in a world, if any.
func (s *Store) PlayerAlliance(ctx context.Context, playerID string, worldID int) (*Alliance, error) {
	var a Alliance
	err := s.DB.QueryRow(ctx, `
		SELECT al.id, al.world_id, al.name, al.tag, al.level, al.created_at,
		       (SELECT count(*) FROM alliance_members m WHERE m.alliance_id=al.id)
		FROM alliances al JOIN alliance_members am ON am.alliance_id=al.id
		WHERE am.player_id=$1 AND am.world_id=$2`, playerID, worldID).
		Scan(&a.ID, &a.WorldID, &a.Name, &a.Tag, &a.Level, &a.CreatedAt, &a.Members)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ---- chat ----

// PostChat inserts a chat message (global or alliance:{id} channel).
func (s *Store) PostChat(ctx context.Context, worldID int, channel, playerID, body string) (ChatMessage, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 500 {
		return ChatMessage{}, fmt.Errorf("message must be 1-500 chars")
	}
	var msg ChatMessage
	err := s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var pname string
		if err := tx.QueryRow(ctx, `SELECT display_name FROM players WHERE id=$1`, playerID).Scan(&pname); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO chat_messages(world_id, channel, player_id, body)
			VALUES($1,$2,$3,$4) RETURNING id, created_at`,
			worldID, channel, playerID, body).Scan(&msg.ID, &msg.CreatedAt)
	})
	if err != nil {
		return ChatMessage{}, err
	}
	msg.Channel = channel
	msg.PlayerID = playerID
	msg.Body = body
	var pname string
	_ = s.DB.QueryRow(ctx, `SELECT display_name FROM players WHERE id=$1`, playerID).Scan(&pname)
	msg.PlayerName = pname
	return msg, nil
}

// ChatHistory returns recent messages for a channel.
func (s *Store) ChatHistory(ctx context.Context, worldID int, channel string, limit int) ([]ChatMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.DB.Query(ctx, `
		SELECT m.id, m.channel, m.player_id, p.display_name, m.body, m.created_at
		FROM chat_messages m JOIN players p ON p.id=m.player_id
		WHERE m.world_id=$1 AND m.channel=$2
		ORDER BY m.id DESC LIMIT $3`, worldID, channel, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.Channel, &m.PlayerID, &m.PlayerName, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	// Return oldest-first for display.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// PruneChat deletes chat older than 7 days (nightly job in the scheduler).
func (s *Store) PruneChat(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM chat_messages WHERE created_at < now() - interval '7 days'`)
	return tag.RowsAffected(), err
}

// PruneIdempotency deletes idempotency rows older than 24h.
func (s *Store) PruneIdempotency(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours'`)
	return tag.RowsAffected(), err
}
