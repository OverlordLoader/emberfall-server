package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// GetOrCreatePlayerByAppleSub returns the player for an Apple `sub`, creating
// one on first sign-in. The Apple sub is the stable player key (never email).
func (s *Store) GetOrCreatePlayerByAppleSub(ctx context.Context, appleSub, displayName string) (Player, bool, error) {
	var p Player
	err := s.DB.QueryRow(ctx,
		`SELECT id, display_name, created_at, banned FROM players WHERE apple_sub=$1`, appleSub).
		Scan(&p.ID, &p.DisplayName, &p.CreatedAt, &p.Banned)
	if err == nil {
		_, _ = s.DB.Exec(ctx, `UPDATE players SET last_seen_at=now() WHERE id=$1`, p.ID)
		return p, false, nil
	}
	if displayName == "" {
		displayName = "Commander"
	}
	p.ID = uuid.NewString()
	p.DisplayName = displayName
	p.CreatedAt = time.Now()
	err = s.DB.QueryRow(ctx,
		`INSERT INTO players(id, apple_sub, display_name) VALUES($1,$2,$3)
		 ON CONFLICT (apple_sub) DO UPDATE SET last_seen_at=now()
		 RETURNING id, display_name, created_at, banned`,
		p.ID, appleSub, displayName).Scan(&p.ID, &p.DisplayName, &p.CreatedAt, &p.Banned)
	if err != nil {
		return Player{}, false, err
	}
	return p, true, nil
}

// GetOrCreateDevPlayer is the DEV/TESTFLIGHT-ONLY auth path. It is gated by
// ALLOW_DEV_AUTH=1 in the API layer and must never be enabled in production.
func (s *Store) GetOrCreateDevPlayer(ctx context.Context, deviceID string) (Player, error) {
	var p Player
	err := s.DB.QueryRow(ctx,
		`SELECT id, display_name, created_at, banned FROM players WHERE device_id=$1`, deviceID).
		Scan(&p.ID, &p.DisplayName, &p.CreatedAt, &p.Banned)
	if err == nil {
		_, _ = s.DB.Exec(ctx, `UPDATE players SET last_seen_at=now() WHERE id=$1`, p.ID)
		return p, nil
	}
	p.ID = uuid.NewString()
	err = s.DB.QueryRow(ctx,
		`INSERT INTO players(id, device_id, display_name) VALUES($1,$2,'Commander')
		 RETURNING id, display_name, created_at, banned`,
		p.ID, deviceID).Scan(&p.ID, &p.DisplayName, &p.CreatedAt, &p.Banned)
	return p, err
}

func (s *Store) GetPlayer(ctx context.Context, playerID string) (Player, error) {
	var p Player
	err := s.DB.QueryRow(ctx,
		`SELECT id, display_name, created_at, banned FROM players WHERE id=$1`, playerID).
		Scan(&p.ID, &p.DisplayName, &p.CreatedAt, &p.Banned)
	return p, err
}

// CreateSession stores a hashed refresh token and returns the raw token.
func (s *Store) CreateSession(ctx context.Context, playerID string, ttl time.Duration) (sessionID, rawRefresh string, err error) {
	rawRefresh = uuid.NewString() + uuid.NewString() // 64 hex chars of entropy
	sum := sha256.Sum256([]byte(rawRefresh))
	sessionID = uuid.NewString()
	_, err = s.DB.Exec(ctx,
		`INSERT INTO sessions(id, player_id, refresh_hash, expires_at) VALUES($1,$2,$3, now()+$4::interval)`,
		sessionID, playerID, hex.EncodeToString(sum[:]), ttl.String())
	return sessionID, rawRefresh, err
}

// ConsumeRefresh validates a refresh token, rotates it (delete + new), and
// returns the player id. Rotating refresh: the old token dies with use.
func (s *Store) ConsumeRefresh(ctx context.Context, rawRefresh string) (playerID string, err error) {
	sum := sha256.Sum256([]byte(rawRefresh))
	hash := hex.EncodeToString(sum[:])
	err = s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var sessionID string
		var expires time.Time
		if err := tx.QueryRow(ctx,
			`SELECT id, player_id, expires_at FROM sessions WHERE refresh_hash=$1`, hash).
			Scan(&sessionID, &playerID, &expires); err != nil {
			return err
		}
		if time.Now().After(expires) {
			return errSessionExpired
		}
		_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, sessionID)
		return err
	})
	return playerID, err
}

// ErrSessionExpired is returned when a refresh token is past its TTL.
var errSessionExpired = errSessionExpiredType{}

type errSessionExpiredType struct{}

func (errSessionExpiredType) Error() string { return "session expired" }

// IsSessionExpired reports whether err is an expired-session error.
func IsSessionExpired(err error) bool { return err == errSessionExpired }

// SetBanned toggles a player's banned flag (admin).
func (s *Store) SetBanned(ctx context.Context, playerID string, banned bool) error {
	_, err := s.DB.Exec(ctx, `UPDATE players SET banned=$2 WHERE id=$1`, playerID, banned)
	return err
}

// FlagCheat inserts an anomaly flag for human review. Never auto-bans.
func (s *Store) FlagCheat(ctx context.Context, q Querier, playerID string, worldID *int, reason string, detail map[string]any) error {
	_, err := q.Exec(ctx,
		`INSERT INTO cheat_flags(player_id, world_id, reason, detail) VALUES($1,$2,$3,$4)`,
		playerID, worldID, reason, detail)
	return err
}
