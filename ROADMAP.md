# Roadmap

Status at a glance: the REST surface was extracted from a production trading
system that runs it against the live exchange daily. What's missing is
breadth — the endpoints and
transports that system didn't need.

## Next

- [ ] WebSocket streams (`trade-api/ws/v2`): orderbook deltas, tickers,
      public trades, and the authenticated fill/position channels. This is
      the biggest gap — today the client is poll-only.
- [ ] Typed `APIError` carrying the HTTP status and the exchange's error
      code, matchable with `errors.As` (today non-2xx responses are plain
      errors with the body attached; `ErrPostOnlyCross` and
      `ErrOrderNotFound` are already typed)
- [ ] Demo-environment runnable example (`DemoBaseURL` and doc coverage
      exist; what's missing is an end-to-end example against a demo account)
- [ ] Events, series, and exchange status/schedule endpoints
- [ ] Tag `v0.1.0`

## Later

- Order amend/decrease endpoints
- Batched order create/cancel
- Portfolio settlements endpoint
- Clock-skew tolerance on request signing
- Jittered retry backoff

Suggestions and reports from anyone running the client against production
are especially welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).
