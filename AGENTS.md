# AGENTS.md

Guidance for coding agents working in this repository.

## What this is

`kalshi-go` is an unofficial Go client library for the Kalshi exchange API
(`api.elections.kalshi.com`, trade-api v2). It was extracted from a
production trading system; wire shapes are verified against the V2 OpenAPI
docs and observed live behavior, with dates cited in code comments.

It is a single flat package (`kalshi`) at the repo root with **zero
dependencies** outside the standard library.

## Commands

```bash
go build ./...
go test -race ./...    # full suite; hermetic, fast, no credentials needed
gofmt -l .             # must print nothing
go vet ./...
staticcheck ./...      # go install honnef.co/go/tools/cmd/staticcheck@latest
```

Run all of the above before considering a change done — CI runs the same set.
Requires Go 1.23+.

## Layout

| File | Contents |
|---|---|
| `doc.go` | Package documentation — the design contract in prose |
| `client.go` | `Client`, public market data (markets, orderbooks, settled history, candlesticks), HTTP core with retry logic |
| `trading.go` | `CreateOrder`, `Positions`, `Balance`, fixed-point count helpers |
| `orders.go` | Resting-order lifecycle: `CancelOrder`, `OpenOrders`, `Fills`, sentinel errors |
| `trades.go` | Public trade tape: `FetchTrades`, `FetchTradesSince` |
| `auth.go` | `Signer` — RSA-PSS request signing (`KALSHI-ACCESS-*` headers) |
| `example_test.go` | Runnable doc examples |

## Invariants — do not break these

These are deliberate design decisions, several paid for with live incidents
(dates cited in comments). They are pinned by tests. Do not "fix" them.

1. **`ClientOrderID` is required on every order.** It is what makes
   retrying a 429 or ambiguous transport failure safe — the exchange
   dedupes on it. `CreateOrder` refuses an order without one.
2. **Counts have two views, and the rounding directions matter.** Orders
   placed here are whole contracts, but counterparties trade fractional
   contracts, so any count the exchange reports about an order can be
   fractional. `Remaining` is the *ceiling* of the exact count and `Filled`
   its *floor* — together they reproduce a whole-contract ledger exactly.
   `*FP` fields carry the exchange's exact values. A fractional count is
   data, never an error.
3. **Strict where money is counted.** A filled order whose
   `average_fill_price` is unusable errors — never a 0¢ cost basis. Fees
   are lenient by choice (advisory field).
4. **Account-wide reads degrade per item, never per page.** `OpenOrders`,
   `Positions`, and `Fills` flag unparseable records via `Malformed` and
   keep going. These reads back reconciliation and emergency-stop paths; a
   page error over one poison record once wedged a live account for 25
   minutes (2026-07-27).
5. **Unauthenticated clients are strictly read-only.** A client from
   `NewClient` holds no credentials and must never touch a trading
   endpoint.
6. **Zero dependencies.** Adding one needs a very strong reason and should
   be raised with the maintainer, not just done.
7. **Cursors are URL-escaped everywhere.** They can contain `+`, `/`, `=`;
   an unescaped `+` decodes server-side as a space and pagination silently
   derails.

## Testing conventions

- Tests are hermetic: HTTP via `net/http/httptest`. Never write a test that
  touches the real API, requires credentials, or could place an order.
- New endpoint decoding gets a test with a realistic wire-format fixture;
  cite the source of the fixture (API docs or observed behavior, with a
  date) in the PR or commit message.
- Keep the invariant-pinning tests passing; if one fails, the change is
  wrong, not the test.

## Style

- Standard Go style; `gofmt` and `staticcheck` clean.
- Exported identifiers get doc comments.
- Wire-format structs are unexported (`createOrderV2Request`,
  `positionsResp`, …) and convert to exported types at the boundary — keep
  that separation.
- Public API is the contract: additive changes are fine pre-v1;
  renames/removals need an upgrade note.
- If behavior described in `README.md` or `doc.go` changes, update the prose
  in the same change.
