# kalshi-go

[![Go Reference](https://pkg.go.dev/badge/github.com/jaracah/kalshi-go.svg)](https://pkg.go.dev/github.com/jaracah/kalshi-go)
[![CI](https://github.com/jaracah/kalshi-go/actions/workflows/ci.yml/badge.svg)](https://github.com/jaracah/kalshi-go/actions/workflows/ci.yml)

Unofficial Go client library for the [Kalshi](https://kalshi.com) exchange
API (trade-api v2). Not affiliated with Kalshi.

This client was extracted from a production trading system that runs it
against the live exchange daily, including the unglamorous edges (cursor
escaping, fractional counterparty fills, malformed-record degradation).
Several of the design notes below were paid for with live incidents.

## Status

**Beta.** The REST surface below has accumulated live production usage, but
the public API of this module may still shift until `v0.1.0` is tagged. WebSocket streams are
not implemented yet — see [ROADMAP.md](ROADMAP.md).

## Install

```bash
go get github.com/jaracah/kalshi-go
```

Requires Go 1.23+. **Zero dependencies** outside the standard library.

Full API documentation is on
[pkg.go.dev](https://pkg.go.dev/github.com/jaracah/kalshi-go).

## Public market data (no credentials)

```go
import kalshi "github.com/jaracah/kalshi-go"

c := kalshi.NewClient(nil)

markets, err := c.DiscoverActive(ctx, "KXBTCD")      // open markets in a series
market, err := c.FetchMarket(ctx, "KXBTCD-26JUL1612-T110000")

yesBid, yesAsk, err := c.FetchOrderbook(ctx, ticker)   // live top-of-book, cents
yes, no, err := c.FetchOrderbookDepth(ctx, ticker)     // full depth

trades, err := c.FetchTrades(ctx, ticker)              // complete public tape
settled, err := c.FetchSettled(ctx, "KXBTCD", 500)   // settled history, cursor-paged
candles, err := c.FetchCandlesticks(ctx, series, ticker, startTS, endTS, 1)
```

A client built with `NewClient` is strictly read-only: it holds no
credentials and never touches a trading endpoint.

Note one live-vs-cached subtlety: the market **summary** endpoint
(`FetchMarket`, `DiscoverActive`) is CloudFront-cached and its quote fields
freeze at the market's open time. The **orderbook** endpoint is not cached —
use `FetchOrderbook`/`FetchOrderbookDepth` for live quotes.

## Trading (authenticated)

Generate an API key in your Kalshi account settings. You get a key ID and an
RSA private key (PEM); requests are signed with `KALSHI-ACCESS-*` headers
automatically per the published spec.

```go
signer, err := kalshi.NewSigner(os.Getenv("KALSHI_KEY_ID"), os.Getenv("KALSHI_PRIVATE_KEY"))
c := kalshi.NewAuthedClient(nil, signer, "") // "" = production; kalshi.DemoBaseURL = demo

// Cheapest authenticated call — a good startup credentials check
bal, err := c.Balance(ctx) // cents

// Take: buy 10 YES at 55¢, immediate-or-cancel
res, err := c.CreateOrder(ctx, kalshi.Order{
    Ticker:        "KXBTCD-26JUL1612-T110000",
    Side:          kalshi.SideBid,
    Count:         10,
    PriceC:        55,
    ClientOrderID: newUUID(), // required; makes retries safe
})
fmt.Println(res.FillCount, res.AvgPriceC, res.TotalFeeC)

// Make: rest a post-only quote (sell YES at 95¢ == buy NO at 5¢)
res, err = c.CreateOrder(ctx, kalshi.Order{
    Ticker:        "KXBTCD-26JUL1612-T110000",
    Side:          kalshi.SideAsk,
    Count:         5,
    PriceC:        95,
    TimeInForce:   kalshi.TIFGoodTillCanceled,
    PostOnly:      true, // reject instead of crossing (ErrPostOnlyCross)
    ClientOrderID: newUUID(),
})

open, err := c.OpenOrders(ctx)                       // every resting order
cres, err := c.CancelOrder(ctx, res.OrderID)         // 404 → ErrOrderNotFound
fills, err := c.Fills(ctx, since)                    // async fill discovery
positions, err := c.Positions(ctx)                   // cursor-paged, whole book
```

Orders use the V2 single-book vocabulary: `SideBid` buys YES, `SideAsk`
sells YES, and prices always quote the YES side (a NO bid at q¢ is the same
order as a YES ask at 100−q¢).

## Design notes

- **Prices parse to integer cents** (`PriceC`, `YesBid`, …) because binary
  contracts trade in 1–99¢ and integer math avoids float drift in P&L.
- **Counts come in two views.** Orders placed through this client are whole
  contracts, but Kalshi counterparties trade *fractional* contracts, so
  every count the exchange reports about your orders can come back
  fractional (a resting 5-lot really does report `"4.82"`). Fields carry
  both a whole-contract view (`Remaining` is the ceiling of the exact
  count, `Filled` its floor — together they reproduce a whole-contract
  ledger exactly) and the exchange's exact value (`RemainingFP`,
  `FilledFP`, `NetYesFP`, …).
- **`ClientOrderID` is required** and is what makes retries safe: the
  exchange dedupes on it, so a 429 or an ambiguous transport failure can be
  retried without double-filling. All requests retry 429s with linear
  backoff.
- **Strict where money is counted, tolerant where it isn't.** A filled
  order whose `average_fill_price` is unusable is an error, never a 0¢ cost
  basis. Account-wide reads (`OpenOrders`, `Positions`, `Fills`) instead
  degrade *per item*: an unparseable record reaches you flagged via its
  `Malformed` field rather than failing the page, because those reads back
  reconciliation and emergency-stop paths that must keep working even when
  one record is poison.
- **Typed sentinel errors** for the two ambiguous outcomes a trading loop
  must branch on: `ErrPostOnlyCross` (benign — the quote didn't rest) and
  `ErrOrderNotFound` (the order is gone; resolve fill-vs-cancel through
  `Fills`, never by assuming).
- **Cursors are URL-escaped.** Kalshi cursors can contain `+`, `/` and `=`;
  unescaped, a `+` decodes server-side as a space and pagination silently
  derails.

## Contributing

Issues and pull requests are welcome — see
[CONTRIBUTING.md](CONTRIBUTING.md) for the checks to run and the invariants
to preserve. The test suite is fully hermetic (`go test -race ./...` needs
no network access or credentials).

## Disclaimer

This is an unofficial client, not affiliated with or endorsed by Kalshi.
Trading involves risk of loss; use at your own risk, and test against small
orders before automating anything with real money.

## License

[MIT](LICENSE)
