// Authenticated trading endpoints: orders, positions, balance. All of these
// require a Signer (NewAuthedClient); the read-only market-data client never
// touches them.
//
// Orders use the V2 shape (POST /portfolio/events/orders, verified against
// the OpenAPI spec 2026-07-16): sides are single-book YES terms — "bid"
// means buy YES, "ask" means sell YES — and prices/counts travel as
// fixed-point decimal strings. The legacy /portfolio/orders endpoint is past
// its announced deprecation window; do not add callers to it.

package kalshi

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Order sides in the V2 single-book vocabulary.
const (
	SideBid = "bid" // buy YES
	SideAsk = "ask" // sell YES (== buy NO at 100−price)
)

// Time-in-force values (verified against the V2 create-order spec
// 2026-07-22; the API also offers fill_or_kill, which nothing here needs).
const (
	TIFImmediateOrCancel = "immediate_or_cancel"
	TIFGoodTillCanceled  = "good_till_canceled"
)

// Order is a limit order in this package's native units (integer cents and
// whole contracts). The zero TimeInForce is IOC: a taker order either fills
// at its snapshot price or misses — if the book moved, missing the fill is
// usually the correct outcome. Pass TIFGoodTillCanceled to rest on the book.
type Order struct {
	Ticker string
	Side   string // SideBid | SideAsk
	Count  int    // whole contracts, > 0
	PriceC int    // YES-side limit price in cents, 1..99
	// TimeInForce: "" (= TIFImmediateOrCancel) or TIFGoodTillCanceled.
	TimeInForce string
	// PostOnly, on a resting order, makes the exchange reject a placement
	// that would cross instead of taking — a maker's join-only invariant
	// enforced exchange-side (verified in the V2 spec 2026-07-22).
	PostOnly bool
	// ClientOrderID makes retries idempotent on the exchange side: a 429 or
	// transport error can be retried with the same id without double-filling.
	ClientOrderID string
}

// OrderResult is the exchange's synchronous answer to an IOC order.
type OrderResult struct {
	OrderID string
	// FillCount is WHOLE contracts filled immediately (0 = book moved, no
	// fill — or a purely fractional fill, which FillCountFP shows).
	FillCount int
	// Remaining is whole contracts still resting for a GTC order, or
	// canceled back for an IOC one.
	Remaining int
	// FillCountFP and RemainingFP are the exchange's exact counts. They
	// differ from the whole-contract fields when a fractional counterparty
	// took part of the lot; callers that book wholes use the ints, callers
	// that must agree with the exchange's own arithmetic use these.
	FillCountFP float64
	RemainingFP float64
	AvgPriceC   int // volume-weighted average fill price, cents (0 when no fill)
	// TotalFeeC is the fee for the whole fill in cents, derived from the
	// exchange's per-contract average (average_fee_paid × the exact
	// fill_count, rounded to the nearest cent) — the closest the V2
	// response gets to an exact total.
	TotalFeeC int
	TSMs      int64 // matching-engine timestamp, epoch ms
}

type createOrderV2Request struct {
	Ticker                  string `json:"ticker"`
	ClientOrderID           string `json:"client_order_id"`
	Side                    string `json:"side"`
	Count                   string `json:"count"`
	Price                   string `json:"price"`
	TimeInForce             string `json:"time_in_force"`
	PostOnly                bool   `json:"post_only,omitempty"`
	SelfTradePreventionType string `json:"self_trade_prevention_type"`
}

type createOrderV2Response struct {
	OrderID        string `json:"order_id"`
	FillCount      string `json:"fill_count"`
	RemainingCount string `json:"remaining_count"`
	AvgFillPrice   string `json:"average_fill_price"`
	AvgFeePaid     string `json:"average_fee_paid"`
	TSMs           int64  `json:"ts_ms"`
}

// CreateOrder places o and reports what executed synchronously (everything,
// for IOC; any immediate crossing fill, for GTC). self_trade_prevention
// stays "taker_at_cross" on every order: on the taker side it can only
// cancel our own taker order, and on the maker side it is the resting order
// a self-cross cancels against — never a fabricated self-fill.
func (c *Client) CreateOrder(ctx context.Context, o Order) (OrderResult, error) {
	if c.signer == nil {
		return OrderResult{}, fmt.Errorf("kalshi: CreateOrder requires an authenticated client")
	}
	if o.Side != SideBid && o.Side != SideAsk {
		return OrderResult{}, fmt.Errorf("kalshi: bad order side %q", o.Side)
	}
	if o.Count <= 0 || o.PriceC < 1 || o.PriceC > 99 {
		return OrderResult{}, fmt.Errorf("kalshi: bad order count=%d priceC=%d", o.Count, o.PriceC)
	}
	if o.ClientOrderID == "" {
		return OrderResult{}, fmt.Errorf("kalshi: ClientOrderID required (it is what makes retries safe)")
	}
	tif := o.TimeInForce
	if tif == "" {
		tif = TIFImmediateOrCancel
	}
	if tif != TIFImmediateOrCancel && tif != TIFGoodTillCanceled {
		return OrderResult{}, fmt.Errorf("kalshi: bad time_in_force %q", tif)
	}
	if o.PostOnly && tif != TIFGoodTillCanceled {
		return OrderResult{}, fmt.Errorf("kalshi: post_only requires a resting time_in_force")
	}
	req := createOrderV2Request{
		Ticker:                  o.Ticker,
		ClientOrderID:           o.ClientOrderID,
		Side:                    o.Side,
		Count:                   strconv.Itoa(o.Count),
		Price:                   centsToDollars(o.PriceC),
		TimeInForce:             tif,
		PostOnly:                o.PostOnly,
		SelfTradePreventionType: "taker_at_cross",
	}
	var resp createOrderV2Response
	if err := c.post(ctx, "/portfolio/events/orders", "create order "+o.Ticker, req, &resp); err != nil {
		if strings.Contains(err.Error(), "post only cross") {
			// The exchange refusing to let a post-only order take: the
			// join-only invariant working, not an order-path failure.
			// Typed so callers can treat it as benign.
			return OrderResult{}, fmt.Errorf("create order %s: %w", o.Ticker, ErrPostOnlyCross)
		}
		return OrderResult{}, err
	}
	fillFP, err := parseCountFP(resp.FillCount)
	if err != nil {
		return OrderResult{}, fmt.Errorf("kalshi: order %s fill_count: %w", resp.OrderID, err)
	}
	remainingFP, err := parseCountFP(resp.RemainingCount)
	if err != nil {
		return OrderResult{}, fmt.Errorf("kalshi: order %s remaining_count: %w", resp.OrderID, err)
	}
	fill := wholesFilled(fillFP)
	out := OrderResult{
		OrderID: resp.OrderID, FillCount: fill, FillCountFP: fillFP,
		Remaining: wholesRemaining(remainingFP), RemainingFP: remainingFP,
		TSMs: resp.TSMs,
	}
	if fillFP > countEpsilon {
		// Any fill is booked into a real-money ledger: parse strictly. A
		// lenient 0 here would record contracts at 0¢ and silently corrupt
		// P&L and the position's cost basis. Keyed off the EXACT count, not
		// wholes: a purely fractional sync fill (a 1-lot filling as 0.95 —
		// observed live 2026-07-24) is still real money at a real price.
		avg, err := strconv.ParseFloat(resp.AvgFillPrice, 64)
		if err != nil || avg <= 0 || avg >= 1 {
			return OrderResult{}, fmt.Errorf("kalshi: order %s filled %s but average_fill_price %q is unusable",
				resp.OrderID, resp.FillCount, resp.AvgFillPrice)
		}
		out.AvgPriceC = int(avg*100 + 0.5)
		// Fees stay lenient BY CHOICE: an absent/garbled fee undercounts P&L
		// by cents, while erroring would leave a real filled order unbooked
		// over a cosmetic field. Computed on the exact count the exchange
		// charged for, not the whole-contract floor. Callers that need exact
		// fees should compute them from Kalshi's published fee schedule
		// instead.
		out.TotalFeeC = int(parseDollars(resp.AvgFeePaid)*fillFP*100 + 0.5)
	}
	return out, nil
}

// Position is one market's exchange-side position.
type Position struct {
	Ticker string
	// NetYes is the whole-contract net position: positive = YES contracts
	// held, negative = NO contracts held (the exchange's position_fp uses
	// the same sign). Rounded toward zero when the position is fractional —
	// NetYesFP carries the exact value.
	NetYes int
	// NetYesFP is position_fp verbatim. A fractional position is ROUTINE,
	// not corruption: it is exactly what the account holds while a
	// whole-contract order is part-filled by a fractional counterparty, and
	// it persists until that order completes, is canceled, or settles.
	// Callers reconciling against a whole-contract ledger must compare
	// against this plus their unbooked piece remainders, never treat the
	// fraction itself as divergence (observed live 2026-07-27).
	NetYesFP float64
	// ExposureC is the exchange's market_exposure in cents — the cost basis
	// of the aggregate position, usable to approximate an average entry
	// price when adopting positions at startup.
	ExposureC int64
	// Malformed is non-empty when position_fp could not be parsed at all —
	// genuine API drift, not a fraction. Per-position, never a page error: a
	// page error here would break startup position adoption and blind every
	// reconcile pass for as long as the position exists (observed live
	// 2026-07-24).
	Malformed string
}

type positionsResp struct {
	Cursor          string `json:"cursor"`
	MarketPositions []struct {
		Ticker          string `json:"ticker"`
		PositionFP      string `json:"position_fp"`
		MarketExposureD string `json:"market_exposure_dollars"`
	} `json:"market_positions"`
}

// Positions returns every market with a non-zero position. It pages through
// the cursor so the caller always sees the whole book.
func (c *Client) Positions(ctx context.Context) ([]Position, error) {
	if c.signer == nil {
		return nil, fmt.Errorf("kalshi: Positions requires an authenticated client")
	}
	var out []Position
	cursor := ""
	for {
		path := "/portfolio/positions?count_filter=position&limit=100"
		if cursor != "" {
			// Cursors are opaque and can contain +, / or = — unescaped, a +
			// decodes server-side as a space and pagination silently derails
			// (same lesson as FetchSettled).
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var pr positionsResp
		if err := c.get(ctx, path, "positions", &pr); err != nil {
			return nil, err
		}
		for _, mp := range pr.MarketPositions {
			netFP, err := parseCountFP(mp.PositionFP)
			var malformed string
			if err != nil {
				malformed = err.Error()
			}
			// Toward zero: a −1.18 NO position holds 1 whole contract the
			// ledger can name, plus 0.18 the fill accumulator is still
			// carrying toward the next one.
			net := int(math.Trunc(netFP + math.Copysign(countEpsilon, netFP)))
			if net == 0 && math.Abs(netFP) < countEpsilon && malformed == "" {
				continue
			}
			out = append(out, Position{
				Ticker:    mp.Ticker,
				NetYes:    net,
				NetYesFP:  netFP,
				ExposureC: int64(parseDollars(mp.MarketExposureD)*100 + 0.5),
				Malformed: malformed,
			})
		}
		if pr.Cursor == "" || len(pr.MarketPositions) == 0 {
			return out, nil
		}
		cursor = pr.Cursor
	}
}

type balanceResp struct {
	Balance int64 `json:"balance"`
}

// Balance returns the member's available balance in cents. Doubling as the
// cheapest authenticated call, it makes a good startup auth check.
func (c *Client) Balance(ctx context.Context) (int64, error) {
	if c.signer == nil {
		return 0, fmt.Errorf("kalshi: Balance requires an authenticated client")
	}
	var br balanceResp
	if err := c.get(ctx, "/portfolio/balance", "balance", &br); err != nil {
		return 0, err
	}
	return br.Balance, nil
}

// centsToDollars renders integer cents as a fixed-point dollar string
// ("0.56", "1.00") — the only price format the V2 endpoints accept.
func centsToDollars(c int) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// parseFixedCount parses a fixed-point contract count ("10.00") into a whole
// int. This client only places whole-contract orders, so where a fraction is
// genuinely impossible a fractional value from the exchange is an error, not
// something to round silently — and every caller reads a REQUIRED field, so
// an empty value is API drift, not zero.
//
// Reach for this ONLY where a fraction is genuinely impossible. Anything the
// exchange reports ABOUT one of your orders can come back fractional,
// because counterparties trade fractional contracts against it — use
// parseCountFP there. Erroring there instead caused a live incident: one
// 5-lot ask resting at remaining_count_fp "4.82" failed the whole OpenOrders
// call for 25 minutes, which blinded reconciliation AND broke cancel-all,
// the emergency stop (2026-07-27).
func parseFixedCount(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty contract count (required field missing)")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if strings.TrimRight(frac, "0") != "" {
		return 0, fmt.Errorf("fractional contract count %q", s)
	}
	n, err := strconv.Atoi(whole)
	if err != nil {
		return 0, fmt.Errorf("bad contract count %q", s)
	}
	return n, nil
}

// parseCountFP parses a fixed-point contract count into its exact value.
// Fractional is normal, not an error: orders placed here are whole, but the
// counterparties filling them are not, so remaining/filled/reduced_by
// counts all carry the residue of somebody else's fractional lot. An empty
// value is still API drift on a required field.
func parseCountFP(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty contract count (required field missing)")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("bad contract count %q", s)
	}
	return v, nil
}

// countEpsilon absorbs float noise in fixed-point counts that arrive as
// two-decimal strings; comparisons and whole-contract rounding are done to
// this tolerance so 0.8199999 never reads as a different quantity from 0.82.
const countEpsilon = 1e-6

// wholesRemaining converts an exchange "still resting" count to the number
// of WHOLE contracts a caller can still expect to book from it, which is
// the ceiling: an order of N whose fills total F rests at N−F, and a
// whole-contract caller's own counter holds N−floor(F) — the same number. A
// 5-lot that took a 0.18 piece rests at 4.82 and can still book 5 wholes,
// because the 0.18 is part of the first of them.
func wholesRemaining(fp float64) int {
	n := int(math.Ceil(fp - countEpsilon))
	if n < 0 {
		return 0
	}
	return n
}

// wholesFilled converts an exchange "filled so far" count to whole booked
// contracts, which is the floor — mirroring a caller that books fractional
// pieces only as each whole completes.
func wholesFilled(fp float64) int {
	n := int(math.Floor(fp + countEpsilon))
	if n < 0 {
		return 0
	}
	return n
}

// parseDollars parses a fixed-point dollar string to float64 (0 on empty or
// garbage — callers use it for advisory fields, never for order pricing).
func parseDollars(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
