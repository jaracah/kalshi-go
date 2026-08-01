# Contributing

Issues and pull requests are welcome. For anything beyond a small fix, consider
opening an issue first so we can agree on the approach before you write code —
and check [ROADMAP.md](ROADMAP.md) first; your idea may already be planned.

## Getting started

The module has no build system beyond the Go toolchain — clone it and go:

```bash
git clone https://github.com/jaracah/kalshi-go
cd kalshi-go
go test ./...
```

The test suite is fully hermetic: every endpoint is exercised against
`httptest` servers, so tests need no network access, no API credentials, and
place no real orders. New code should follow the same pattern — a test that
dials `api.elections.kalshi.com` will not be accepted.

## Before sending a PR

```bash
gofmt -l .        # must print nothing
go vet ./...
staticcheck ./...  # go install honnef.co/go/tools/cmd/staticcheck@latest
go test -race ./...
```

CI runs the same checks.

## Ground rules

- **Zero dependencies.** The module uses only the standard library; adding a
  dependency needs a very strong reason.
- **Don't break the money invariants.** `ClientOrderID` must stay required
  (it is what makes retries safe), fields that feed P&L (fill prices) must
  fail loudly rather than degrade to zero, and account-wide reads
  (`OpenOrders`, `Positions`, `Fills`) must degrade per item via `Malformed`
  fields — never fail a whole page over one bad record. Tests pin these —
  keep them passing.
- **API changes:** exported types and signatures are the public contract.
  Additive changes are fine pre-v1; renames/removals should come with a clear
  upgrade note in the PR description.
- **Wire-format changes:** cite your source — the Kalshi API docs
  (docs.kalshi.com / the trade-api v2 OpenAPI spec) or observed API
  behavior — in the PR description, as the existing code comments do.
- **Documentation:** exported identifiers get doc comments; if you change
  behavior described in the README or `doc.go`, update those too.

## Working with coding agents

Contributions written with the help of a coding agent (Claude Code, Cursor,
Copilot, etc.) are fine. [AGENTS.md](AGENTS.md) gives agents the project context
— the same checks and invariants above apply, and you are responsible for
reviewing what you submit.
