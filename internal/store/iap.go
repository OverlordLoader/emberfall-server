package store

import (
	"context"
	"fmt"
)

// ---- IAP product catalog (server side) ----
//
// The iOS client is fully ad-free, so there is deliberately no "Remove Ads"
// product here (a non-functional remove-ads grant would be dead code and a
// confusion risk). The three products below must match App Store Connect and
// the client's StoreManager exactly:
//
//   - app.emberfall.game.bundle.speedup .. consumable $1.99 — 3x 15-min speedups
//   - app.emberfall.game.summon.epic10 .. consumable $4.99 — 10x commander summon
//   - app.emberfall.game.bundle.warden .. consumable $4.99 — Warden's Cache:
//     8x 15-min speedups + 3 guaranteed epic summons
//
// Fulfillment is via the inventory (GrantItems) plus direct commander grants
// for guaranteed epics. The phase-2 App Store receipt-verification endpoint
// calls FulfillIAPProduct after verifying the receipt server-side; until
// then, POST /v1/admin/grant covers manual/test fulfillment.

// Product IDs (must match App Store Connect exactly).
const (
	ProductBundleSpeedup = "app.emberfall.game.bundle.speedup"
	ProductSummonEpic10  = "app.emberfall.game.summon.epic10"
	ProductBundleWarden  = "app.emberfall.game.bundle.warden"
)

// IAPProductGrants maps each product to the inventory items it grants.
// Commander summons that bypass tickets are NOT items — see
// IAPProductEpicSummons.
var IAPProductGrants = map[string]map[string]int{
	ProductBundleSpeedup: {"speedup_15m": 3},
	ProductSummonEpic10:  {"summon_ticket": 10},
	ProductBundleWarden:  {"speedup_15m": 8},
}

// IAPProductEpicSummons maps products to the number of guaranteed epic
// commander summons they confer (granted via grantEpicCommanders, which
// honors the pity convention).
var IAPProductEpicSummons = map[string]int{
	ProductBundleWarden: 3,
}

// FulfillIAPProduct grants everything a verified purchase of productID
// confers: inventory items plus any guaranteed epic commander summons.
// Unknown product IDs are rejected — the client only ever sells the three
// products above, so anything else is a bug or tampering.
func (s *Store) FulfillIAPProduct(ctx context.Context, playerID string, productID string) error {
	items, ok := IAPProductGrants[productID]
	if !ok {
		return fmt.Errorf("unknown IAP product %q", productID)
	}
	return s.TxFunc(ctx, func(ctx context.Context, tx Querier) error {
		var worldID int
		if err := tx.QueryRow(ctx,
			`SELECT world_id FROM cities WHERE player_id=$1 ORDER BY created_at LIMIT 1`,
			playerID).Scan(&worldID); err != nil {
			return fmt.Errorf("join a world first")
		}
		if err := s.GrantItems(ctx, tx, playerID, worldID, items); err != nil {
			return err
		}
		if n := IAPProductEpicSummons[productID]; n > 0 {
			if err := s.grantEpicCommanders(ctx, tx, playerID, worldID, n); err != nil {
				return err
			}
		}
		return nil
	})
}
