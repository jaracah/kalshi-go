package kalshi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// DefaultBaseURL is Kalshi's public trade-api v2 root.
const DefaultBaseURL = "https://api.elections.kalshi.com/trade-api/v2"

// Client reads public market data, and — when built by NewAuthedClient —
// places orders and reads the portfolio. The zero value is not usable.
type Client struct {
	hc      *http.Client
	baseURL string
	signer  *Signer // nil = read-only public client
}

// NewClient returns a read-only Client using hc (or a sane default if hc is nil).
func NewClient(hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{hc: hc, baseURL: DefaultBaseURL}
}

// NewAuthedClient returns a Client that signs every request with signer,
// unlocking the trading endpoints. baseURL "" means production; tests and
// the demo environment pass their own.
func NewAuthedClient(hc *http.Client, signer *Signer, baseURL string) *Client {
	c := NewClient(hc)
	c.signer = signer
	if baseURL != "" {
		c.baseURL = baseURL
	}
	return c
}

// Market is one market, with the fields this client decodes.
type Market struct {
	Ticker string `json:"ticker"`
	Title  string `json:"title"`
	// This endpoint reports quotes as dollar-denominated strings ("0.2400"),
	// not integer-cent fields. NOTE: every summary field here — including these
	// quotes and volume_fp — freezes at the window's open_time (the response is
	// CloudFront-cached and the object isn't refreshed intra-window). For a live
	// quote use FetchOrderbook; treat VolumeFP as open-time only.
	YesBidDollars string `json:"yes_bid_dollars"`
	YesAskDollars string `json:"yes_ask_dollars"`
	VolumeFP      string `json:"volume_fp"`
	Status        string `json:"status"`
	Result        string `json:"result"`     // "yes" | "no" | ""
	OpenTime      string `json:"open_time"`  // ISO-8601, window start
	CloseTime     string `json:"close_time"` // ISO-8601, window end

	// Derived from the *_dollars strings by normalize(); callers work in cents.
	YesBid int // cents, 0-100
	YesAsk int // cents, 0-100
}

// normalize fills the integer-cent fields from the dollar strings the API
// actually returns. Called once after decoding any market.
func (m *Market) normalize() {
	m.YesBid = dollarsToCents(m.YesBidDollars)
	m.YesAsk = dollarsToCents(m.YesAskDollars)
}

type marketResp struct {
	Market Market `json:"market"`
}

type marketsResp struct {
	Markets []Market `json:"markets"`
	Cursor  string   `json:"cursor"`
}

// orderbookResp mirrors GET /markets/{ticker}/orderbook. Levels are
// [price_dollars, size] string pairs. The "yes" side holds resting bids to buy
// YES; the "no" side holds resting bids to buy NO. This endpoint is NOT
// CloudFront-cached, so unlike the market summary's yes_bid/yes_ask fields it
// reflects the live book.
type orderbookResp struct {
	Orderbook struct {
		Yes [][]string `json:"yes_dollars"`
		No  [][]string `json:"no_dollars"`
	} `json:"orderbook_fp"`
}

// get issues a GET to path (relative to baseURL) and decodes the JSON body into
// out. It retries a 429 a few times with linear backoff (the candlestick
// endpoint is rate-limited aggressively); other non-200s fail immediately. what
// labels the request in error messages.
func (c *Client) get(ctx context.Context, path, what string, out any) error {
	return c.do(ctx, http.MethodGet, path, what, nil, out)
}

// post issues a signed JSON POST. Retrying a POST is safe here for the same
// reason it is safe for get: every order carries a client_order_id, so a
// retry after a 429 (or an ambiguous transport failure) cannot double-fill.
func (c *Client) post(ctx context.Context, path, what string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: encode body: %w", what, err)
	}
	return c.do(ctx, http.MethodPost, path, what, payload, out)
}

func (c *Client) do(ctx context.Context, method, path, what string, body []byte, out any) error {
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
		if err != nil {
			return fmt.Errorf("%s: build request: %w", what, err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.signer != nil {
			// Signed per attempt: the signature embeds a fresh timestamp.
			if err := c.signer.sign(req); err != nil {
				return err
			}
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			lastErr = fmt.Errorf("rate limited (429) on %s", what)
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			// The error body carries the exchange's reason (insufficient
			// balance, market closed, ...) — surface it, it is the operator's
			// only clue.
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return fmt.Errorf("%s: status %d: %s", what, resp.StatusCode, bytes.TrimSpace(msg))
		}
		err = json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		return err
	}
	return lastErr
}

// FetchMarket returns the current summary for one market.
func (c *Client) FetchMarket(ctx context.Context, ticker string) (Market, error) {
	var mr marketResp
	if err := c.get(ctx, "/markets/"+ticker, ticker, &mr); err != nil {
		return Market{}, err
	}
	mr.Market.normalize()
	return mr.Market, nil
}

// DiscoverActive returns open markets in a series, soonest close_time first.
func (c *Client) DiscoverActive(ctx context.Context, series string) ([]Market, error) {
	path := fmt.Sprintf("/markets?series_ticker=%s&status=open&limit=100", url.QueryEscape(series))
	var mr marketsResp
	if err := c.get(ctx, path, "series "+series, &mr); err != nil {
		return nil, err
	}
	for i := range mr.Markets {
		mr.Markets[i].normalize()
	}
	sort.Slice(mr.Markets, func(i, j int) bool {
		return mr.Markets[i].CloseTime < mr.Markets[j].CloseTime
	})
	return mr.Markets, nil
}

// FetchOrderbook returns live top-of-book in cents: yesBid is the best price you
// could SELL yes at (highest yes bid); yesAsk is the best price you could BUY
// yes at, derived as 100 - bestNoBid (a No bid at q is a Yes offer at 100-q).
// Either is 0 when that side of the book is empty.
func (c *Client) FetchOrderbook(ctx context.Context, ticker string) (yesBid, yesAsk int, err error) {
	var ob orderbookResp
	if err := c.get(ctx, "/markets/"+ticker+"/orderbook", "orderbook "+ticker, &ob); err != nil {
		return 0, 0, err
	}
	yesBid = bestLevelCents(ob.Orderbook.Yes)
	if bestNo := bestLevelCents(ob.Orderbook.No); bestNo > 0 {
		yesAsk = 100 - bestNo
	}
	return yesBid, yesAsk, nil
}

// Level is one resting orderbook level, in this package's native units.
type Level struct {
	PriceC int // cents
	Size   int // displayed contracts
}

// FetchOrderbookDepth returns the live book's full depth, best-priced level
// first on each side. The yes side holds resting bids to buy YES; the no side
// holds resting bids to buy NO (a NO bid at q is a YES offer at 100−q). The
// best NO level's size is the queue a joining maker rests behind. Like
// FetchOrderbook this endpoint is not CloudFront-cached.
func (c *Client) FetchOrderbookDepth(ctx context.Context, ticker string) (yes, no []Level, err error) {
	var ob orderbookResp
	if err := c.get(ctx, "/markets/"+ticker+"/orderbook", "orderbook "+ticker, &ob); err != nil {
		return nil, nil, err
	}
	return parseLevels(ob.Orderbook.Yes), parseLevels(ob.Orderbook.No), nil
}

// parseLevels decodes [price_dollars, size] string pairs, best-priced first.
// Malformed levels are skipped, matching the tolerance of bestLevelCents.
func parseLevels(raw [][]string) []Level {
	var out []Level
	for _, lvl := range raw {
		if len(lvl) < 2 {
			continue
		}
		p, err1 := strconv.ParseFloat(lvl[0], 64)
		s, err2 := strconv.ParseFloat(lvl[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, Level{PriceC: int(p*100 + 0.5), Size: int(s)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PriceC > out[j].PriceC })
	return out
}

// FetchSettled returns up to limit recently-settled markets in a series, newest
// first (each carries a Result of "yes"/"no" and its open/close times). It pages
// with the API cursor so limit may exceed the 100-per-request cap — letting
// callers reach older history.
func (c *Client) FetchSettled(ctx context.Context, series string, limit int) ([]Market, error) {
	var out []Market
	cursor := ""
	for len(out) < limit {
		page := limit - len(out)
		if page > 100 {
			page = 100
		}
		path := fmt.Sprintf("/markets?series_ticker=%s&status=settled&limit=%d", url.QueryEscape(series), page)
		if cursor != "" {
			// Cursors are opaque and can contain +, / or = — unescaped, a +
			// decodes server-side as a space and pagination silently derails.
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var mr marketsResp
		if err := c.get(ctx, path, "settled "+series, &mr); err != nil {
			return out, err
		}
		for i := range mr.Markets {
			mr.Markets[i].normalize()
		}
		out = append(out, mr.Markets...)
		if mr.Cursor == "" || len(mr.Markets) == 0 {
			break // no more pages
		}
		cursor = mr.Cursor
	}
	return out, nil
}

// Candle is one period's quote, reduced to closing top-of-book in cents.
type Candle struct {
	EndTS  int64 // unix seconds, period end
	YesBid int   // close, cents (0 if no bid)
	YesAsk int   // close, cents (0 if no ask)
}

type ohlc struct {
	Close string `json:"close_dollars"`
}

type candlesResp struct {
	Candlesticks []struct {
		EndPeriodTS int64 `json:"end_period_ts"`
		YesBid      ohlc  `json:"yes_bid"`
		YesAsk      ohlc  `json:"yes_ask"`
	} `json:"candlesticks"`
}

// FetchCandlesticks returns the historical quote series for a market over
// [startTS, endTS] (unix seconds) at periodMin-minute resolution. Quotes are
// the period close (the open carries occasional pre-trade artifacts).
func (c *Client) FetchCandlesticks(ctx context.Context, series, ticker string, startTS, endTS int64, periodMin int) ([]Candle, error) {
	path := fmt.Sprintf("/series/%s/markets/%s/candlesticks?start_ts=%d&end_ts=%d&period_interval=%d",
		series, ticker, startTS, endTS, periodMin)
	var cr candlesResp
	if err := c.get(ctx, path, "candlesticks "+ticker, &cr); err != nil {
		return nil, err
	}
	out := make([]Candle, 0, len(cr.Candlesticks))
	for _, c := range cr.Candlesticks {
		out = append(out, Candle{
			EndTS:  c.EndPeriodTS,
			YesBid: dollarsToCents(c.YesBid.Close),
			YesAsk: dollarsToCents(c.YesAsk.Close),
		})
	}
	return out, nil
}

// PickLive returns the soonest-closing market that is actually tradeable right
// now: its window has opened and it is not within buffer of its close. markets
// is assumed sorted by close_time ascending (as DiscoverActive returns them).
// Markets with unparseable times are skipped. ok is false when nothing is
// currently in-window.
func PickLive(markets []Market, now time.Time, buffer time.Duration) (m Market, ok bool) {
	for _, mk := range markets {
		ct, err := time.Parse(time.RFC3339, mk.CloseTime)
		if err != nil {
			continue // can't tell if it's live; skip rather than trade blind
		}
		// Skip markets already past close (or within the buffer) — these carry
		// frozen, near-settlement quotes.
		if !now.Before(ct.Add(-buffer)) {
			continue
		}
		// If we know the open time, require the window to have started.
		if mk.OpenTime != "" {
			if ot, err := time.Parse(time.RFC3339, mk.OpenTime); err == nil && now.Before(ot) {
				continue // hasn't opened yet
			}
		}
		return mk, true
	}
	return Market{}, false
}

// dollarsToCents parses "0.2400" -> 24 (nearest cent).
func dollarsToCents(s string) int {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(v*100 + 0.5)
}

// bestLevelCents returns the highest price across [price_dollars, size] levels,
// in nearest cents (0 if the book side is empty).
func bestLevelCents(levels [][]string) int {
	best := 0.0
	for _, lvl := range levels {
		if len(lvl) == 0 {
			continue
		}
		p, err := strconv.ParseFloat(lvl[0], 64)
		if err == nil && p > best {
			best = p
		}
	}
	return int(best*100 + 0.5)
}
