// Package auth verifies Sign in with Apple identity tokens and mints the
// game's own session tokens (short-lived access JWT + rotating refresh).
package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AppleClaims are the claims we check on an Apple identity token.
type AppleClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Subject  string `json:"sub"`
	Expiry   int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
	Email    string `json:"email"`
}

type jwks struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// Verifier verifies Apple identity tokens against Apple's JWKS.
type Verifier struct {
	servicesID string
	jwksURL    string
	client     *http.Client
	mu         sync.Mutex
	keys       map[string]*rsa.PublicKey
	fetchedAt  time.Time
}

// NewVerifier builds an Apple token verifier for the given Services ID (aud).
func NewVerifier(servicesID string) *Verifier {
	return &Verifier{
		servicesID: servicesID,
		jwksURL:    "https://appleid.apple.com/auth/keys",
		client:     &http.Client{Timeout: 10 * time.Second},
		keys:       map[string]*rsa.PublicKey{},
	}
}

// Verify checks signature (RS256 vs Apple JWKS), iss, aud, and exp.
// Returns the claims; the caller uses sub as the stable player key.
func (v *Verifier) Verify(ctx context.Context, identityToken string) (AppleClaims, error) {
	var claims AppleClaims
	parts := strings.Split(identityToken, ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("malformed token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, fmt.Errorf("bad header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return claims, err
	}
	if header.Alg != "RS256" {
		return claims, fmt.Errorf("unexpected alg %s", header.Alg)
	}
	pub, err := v.key(ctx, header.Kid)
	if err != nil {
		return claims, err
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, fmt.Errorf("bad signature: %w", err)
	}
	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], sig); err != nil {
		return claims, fmt.Errorf("signature invalid: %w", err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, err
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, err
	}
	if claims.Issuer != "https://appleid.apple.com" {
		return claims, fmt.Errorf("bad iss %s", claims.Issuer)
	}
	if claims.Audience != v.servicesID {
		return claims, fmt.Errorf("bad aud %s", claims.Audience)
	}
	if claims.Subject == "" {
		return claims, fmt.Errorf("missing sub")
	}
	if time.Now().Unix() > claims.Expiry {
		return claims, fmt.Errorf("token expired")
	}
	return claims, nil
}

func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok && time.Since(v.fetchedAt) < time.Hour {
		return k, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", v.jwksURL, nil)
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	var set jwks
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	v.keys = map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 + int(b)
		}
		v.keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}
	}
	v.fetchedAt = time.Now()
	k, ok := v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("unknown kid %s", kid)
	}
	return k, nil
}

// ---- game session JWTs (HS256, 15 min) ----

// AccessClaims are carried in the short-lived access JWT.
type AccessClaims struct {
	PlayerID string `json:"pid"`
	WorldID  *int   `json:"wid,omitempty"`
	Expiry   int64  `json:"exp"`
	JTI      string `json:"jti"`
}

// Signer mints and verifies access JWTs.
type Signer struct {
	secret []byte
}

// NewSigner builds a JWT signer from the JWT_SECRET env value.
func NewSigner(secret string) *Signer { return &Signer{secret: []byte(secret)} }

// MintAccess creates a 15-minute access token.
func (s *Signer) MintAccess(playerID string, worldID *int) (string, error) {
	claims := AccessClaims{
		PlayerID: playerID,
		WorldID:  worldID,
		Expiry:   time.Now().Add(15 * time.Minute).Unix(),
		JTI:      uuid.NewString(),
	}
	return s.encode(claims)
}

// VerifyAccess checks signature + expiry and returns the claims.
func (s *Signer) VerifyAccess(token string) (AccessClaims, error) {
	var claims AccessClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("malformed token")
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, err
	}
	want := hmacSHA256([]byte(signingInput), s.secret)
	if !hmacEqual(sig, want) {
		return claims, fmt.Errorf("bad signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, err
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, err
	}
	if time.Now().Unix() > claims.Expiry {
		return claims, fmt.Errorf("token expired")
	}
	if claims.PlayerID == "" {
		return claims, fmt.Errorf("missing player")
	}
	return claims, nil
}

func (s *Signer) encode(claims AccessClaims) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := header + "." + payload
	sig := hmacSHA256([]byte(signingInput), s.secret)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
