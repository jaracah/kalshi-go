package kalshi

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Trade is one print from the public trade tape (GET /markets/trades).
// taker_side is only "which side crossed the spread" — the tape does not say
// whether the taker was informed.
type Trade struct {
	TradeID         string `json:"trade_id"`
	CreatedTime     string `json:"created_time"`
	TakerSide       string `json:"taker_side"` // "yes" | "no"
	CountFP         string `json:"count_fp"`
	YesPriceDollars string `json:"yes_price_dollars"`

	// Derived from the wire strings by normalize; callers use these. Excluded
	// from JSON so cached tapes round-trip through the wire fields only.
	Time     time.Time `json:"-"`
	Count    float64   `json:"-"` // contracts; the API reports fractions ("935.19")
	YesCents int       `json:"-"`
}

func (t *Trade) normalize() {
	t.Time, _ = time.Parse(time.RFC3339, t.CreatedTime)
	t.Count, _ = strconv.ParseFloat(t.CountFP, 64)
	t.YesCents = dollarsToCents(t.YesPriceDollars)
}

// NormalizeTrades fills the derived fields on trades decoded from a cached
// tape (which holds only the wire strings) and sorts them oldest-first.
func NormalizeTrades(trades []Trade) {
	for i := range trades {
		trades[i].normalize()
	}
	sort.Slice(trades, func(i, j int) bool { return trades[i].Time.Before(trades[j].Time) })
}

type tradesResp struct {
	Trades []Trade `json:"trades"`
	Cursor string  `json:"cursor"`
}

// FetchTrades returns the complete public tape for ticker, oldest first,
// paging with the API cursor (1000 trades per page, brief pause between
// pages). The page cap is a runaway guard far above any real single-market
// tape; hitting it is an error — a silently truncated tape would corrupt
// downstream analysis.
func (c *Client) FetchTrades(ctx context.Context, ticker string) ([]Trade, error) {
	const maxPages = 400
	var out []Trade
	cursor := ""
	for page := range maxPages {
		if page > 0 {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		path := fmt.Sprintf("/markets/trades?ticker=%s&limit=1000", url.QueryEscape(ticker))
		if cursor != "" {
			// Cursors are opaque and can contain +, / or = — see FetchSettled.
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var tr tradesResp
		if err := c.get(ctx, path, "trades "+ticker, &tr); err != nil {
			return out, err
		}
		out = append(out, tr.Trades...)
		if tr.Cursor == "" || len(tr.Trades) == 0 {
			NormalizeTrades(out)
			return out, nil
		}
		cursor = tr.Cursor
	}
	return nil, fmt.Errorf("trades %s: still paging after %d pages (%d trades) — refusing to return a truncated tape", ticker, maxPages, len(out))
}

// FetchTradesSince returns the ticker's prints strictly after since, oldest
// first — the incremental form a fill-polling loop wants (refetching whole
// tapes every pass would hammer the API). The page cap bounds one poll; a
// capped read returns what it has with an error so the caller can treat the
// tape as possibly-incomplete rather than silently truncated.
func (c *Client) FetchTradesSince(ctx context.Context, ticker string, since time.Time) ([]Trade, error) {
	const maxPages = 40
	var out []Trade
	cursor := ""
	for page := range maxPages {
		if page > 0 {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		path := fmt.Sprintf("/markets/trades?ticker=%s&limit=1000&min_ts=%d",
			url.QueryEscape(ticker), since.Unix())
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var tr tradesResp
		if err := c.get(ctx, path, "trades "+ticker, &tr); err != nil {
			return out, err
		}
		for _, t := range tr.Trades {
			t.normalize()
			// min_ts is second-granular; enforce the strict boundary here so
			// callers can hand back their last-seen instant verbatim.
			if t.Time.After(since) {
				out = append(out, t)
			}
		}
		if tr.Cursor == "" || len(tr.Trades) == 0 {
			sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
			return out, nil
		}
		cursor = tr.Cursor
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, fmt.Errorf("trades %s: still paging after %d pages (%d trades) — window too wide for one poll", ticker, maxPages, len(out))
}
