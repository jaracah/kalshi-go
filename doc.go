// Package kalshi is an unofficial Go client for Kalshi's trade-api v2
// (api.elections.kalshi.com). It is not affiliated with Kalshi.
//
// Without a Signer the client is strictly read-only public market data
// (NewClient — no credentials, nothing is ever sent to trading
// endpoints). With one (NewAuthedClient) the trading endpoints become
// available:
//
//	c := kalshi.NewClient(nil)
//	yesBid, yesAsk, err := c.FetchOrderbook(ctx, "KXBTCD-26JUL1612-T110000")
//
//	signer, err := kalshi.NewSigner(keyID, pemKey)
//	c = kalshi.NewAuthedClient(nil, signer, "")
//	res, err := c.CreateOrder(ctx, kalshi.Order{...})
//
// Requests are signed per Kalshi's published API-key spec: RSA-PSS
// (SHA-256) over timestamp+method+path, sent in KALSHI-ACCESS-* headers.
//
// # Prices and counts
//
// Prices cross the wire as fixed-point dollar strings ("0.5600"). This
// package parses them to integer cents (PriceC, YesBid, ...) because
// binary contracts trade in 1–99¢ and integer math avoids float drift
// in P&L. Contract counts are subtler: orders placed through this client
// are whole contracts, but Kalshi counterparties trade fractional
// contracts, so every count the exchange reports about your orders —
// fills, remaining, positions — can come back fractional. Such fields
// carry both a whole-contract view (Remaining is the ceiling of the
// exact count, Filled its floor; together they reproduce a
// whole-contract ledger exactly) and the exchange's exact fixed-point
// value (RemainingFP, FilledFP, NetYesFP, ...).
//
// # Sides
//
// Orders use the V2 single-book vocabulary: SideBid buys YES, SideAsk
// sells YES. A NO bid at q¢ is the same order as a YES ask at 100−q¢;
// prices always quote the YES side.
//
// # Retries and idempotency
//
// Every order requires a ClientOrderID — it is what makes retries safe
// on the exchange side — and CreateOrder refuses to place one without
// it. All requests retry 429s with linear backoff. Canceling an order
// the exchange no longer knows returns ErrOrderNotFound (wrapped); the
// caller resolves whether it filled or was already canceled through
// Fills, never by assuming.
//
// # Malformed data
//
// Fields that feed money math parse strictly: a filled order whose
// average price is unusable is an error, never a 0¢ cost basis.
// Account-wide reads (OpenOrders, Positions, Fills) instead degrade per
// item — an unparseable order, position, or fill reaches the caller
// flagged via its Malformed field rather than failing the page, because
// those reads back reconciliation and emergency-stop paths that must
// keep working even when one record is poison.
package kalshi
