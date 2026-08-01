// Resting-order lifecycle endpoints: cancel, open-order listing, and async
// fill discovery. Shapes verified against the V2 OpenAPI docs 2026-07-22
// (cancel-order-v2, get-orders, get-fills), same discipline as trading.go's
// 2026-07-16 note.

package kalshi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrPostOnlyCross marks a post-only order the exchange refused because it
// would have crossed the book and taken — the join-only invariant enforced
// server-side. Benign and self-limiting: the quote simply does not rest.
var ErrPostOnlyCross = errors.New("kalshi: post-only order would cross")

// ErrOrderNotFound marks a cancel whose order the exchange no longer knows
// as resting (already fully filled or already canceled). The caller must
// resolve the ambiguity through the fills endpoint, never by assuming.
var ErrOrderNotFound = errors.New("kalshi: order not found")

// CancelResult is the exchange's answer to an order cancel.
type CancelResult struct {
	OrderID       string
	ClientOrderID string
	// ReducedBy is how many whole contracts the cancel removed — the
	// remaining count at cancellation time, rounded the same way
	// OpenOrder.Remaining is. Anything below the caller's believed remaining
	// filled (or was reduced) before the cancel landed: that difference is a
	// real position, discovered via Fills, never retried away.
	ReducedBy int
	// ReducedByFP is the exact reduced_by. It goes fractional when a
	// counterparty took part of the lot before the cancel landed; the
	// caller compares against it to notice pieces too small to move
	// ReducedBy.
	ReducedByFP float64
	TSMs        int64
}

type cancelOrderV2Response struct {
	OrderID       string `json:"order_id"`
	ClientOrderID string `json:"client_order_id"`
	ReducedBy     string `json:"reduced_by"`
	TSMs          int64  `json:"ts_ms"`
}

// CancelOrder cancels a resting order by id. A 404 returns ErrOrderNotFound
// (wrapped): the order is gone from the book — filled or already canceled —
// and the caller owes a fills poll to learn which.
func (c *Client) CancelOrder(ctx context.Context, orderID string) (CancelResult, error) {
	if c.signer == nil {
		return CancelResult{}, fmt.Errorf("kalshi: CancelOrder requires an authenticated client")
	}
	if orderID == "" {
		return CancelResult{}, fmt.Errorf("kalshi: CancelOrder requires an order id")
	}
	var resp cancelOrderV2Response
	err := c.do(ctx, http.MethodDelete, "/portfolio/events/orders/"+url.PathEscape(orderID),
		"cancel order "+orderID, nil, &resp)
	if err != nil {
		if strings.Contains(err.Error(), "status 404") {
			return CancelResult{}, fmt.Errorf("cancel order %s: %w", orderID, ErrOrderNotFound)
		}
		return CancelResult{}, err
	}
	reducedFP, err := parseCountFP(resp.ReducedBy)
	if err != nil {
		return CancelResult{}, fmt.Errorf("kalshi: cancel %s reduced_by: %w", orderID, err)
	}
	return CancelResult{
		OrderID: resp.OrderID, ClientOrderID: resp.ClientOrderID,
		ReducedBy: wholesRemaining(reducedFP), ReducedByFP: reducedFP, TSMs: resp.TSMs,
	}, nil
}

// OpenOrder is one resting order, carrying both whole-contract and exact
// fixed-point counts.
type OpenOrder struct {
	OrderID       string
	ClientOrderID string
	Ticker        string
	BookSide      string // SideBid | SideAsk (the V2 book vocabulary)
	YesPriceC     int    // YES-side limit price, cents
	Remaining     int    // WHOLE contracts that can still fill from this order
	Filled        int    // whole contracts filled so far
	CreatedTime   string // ISO-8601, exchange-reported
	// RemainingFP and FilledFP are the exchange's exact counts, fractional
	// whenever a counterparty has taken part of a lot. Remaining is their
	// ceiling and Filled their floor, which together reproduce a
	// whole-contract caller's view exactly: a 5-lot part-filled by 0.18
	// rests at 4.82 == 5 wholes still to book, 0 booked so far.
	RemainingFP float64
	FilledFP    float64
	// Malformed is non-empty when a count could not be parsed at all. The
	// order still reaches the caller — its id is enough to cancel it — but
	// its counts are zero and must not be trusted.
	Malformed string
}

type openOrdersResp struct {
	Orders []struct {
		OrderID        string `json:"order_id"`
		ClientOrderID  string `json:"client_order_id"`
		Ticker         string `json:"ticker"`
		BookSide       string `json:"book_side"`
		YesPriceD      string `json:"yes_price_dollars"`
		FillCountFP    string `json:"fill_count_fp"`
		RemainingCntFP string `json:"remaining_count_fp"`
		CreatedTime    string `json:"created_time"`
	} `json:"orders"`
	Cursor string `json:"cursor"`
}

// OpenOrders returns every resting order, cursor-paged — the exchange's
// order-side truth for adoption and reconciliation.
func (c *Client) OpenOrders(ctx context.Context) ([]OpenOrder, error) {
	if c.signer == nil {
		return nil, fmt.Errorf("kalshi: OpenOrders requires an authenticated client")
	}
	var out []OpenOrder
	cursor := ""
	for {
		path := "/portfolio/orders?status=resting&limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var or openOrdersResp
		if err := c.get(ctx, path, "open orders", &or); err != nil {
			return nil, err
		}
		for _, o := range or.Orders {
			// Per-order degradation, never a page error. This is the
			// account-wide read that reconciliation, startup adoption and
			// cancel-all flows all depend on: failing the page over one
			// order's unparseable field takes the emergency stop down with
			// it, which is exactly how a single "4.82" wedged a live book
			// for 25 minutes on 2026-07-27. A bad order still reaches the
			// caller, carrying Malformed and an id that can always be
			// canceled.
			oo := OpenOrder{
				OrderID: o.OrderID, ClientOrderID: o.ClientOrderID, Ticker: o.Ticker,
				BookSide: o.BookSide, YesPriceC: dollarsToCents(o.YesPriceD),
				CreatedTime: o.CreatedTime,
			}
			remainingFP, rErr := parseCountFP(o.RemainingCntFP)
			filledFP, fErr := parseCountFP(o.FillCountFP)
			switch {
			case rErr != nil:
				oo.Malformed = "remaining: " + rErr.Error()
			case fErr != nil:
				oo.Malformed = "fill_count: " + fErr.Error()
			default:
				oo.RemainingFP, oo.FilledFP = remainingFP, filledFP
				oo.Remaining = wholesRemaining(remainingFP)
				oo.Filled = wholesFilled(filledFP)
			}
			out = append(out, oo)
		}
		if or.Cursor == "" || len(or.Orders) == 0 {
			return out, nil
		}
		cursor = or.Cursor
	}
}

// Fill is one portfolio fill — the async discovery record carrying the
// exchange-reported price and fee.
type Fill struct {
	FillID   string
	OrderID  string
	Ticker   string
	BookSide string // SideBid | SideAsk
	IsTaker  bool
	// Count is the whole-contract count when the exchange reported an
	// integral fill, else 0. CountFP is always the exact reported count:
	// counterparties trade fractional contracts, so YOUR integral order can
	// fill in fractional pieces (observed live 2026-07-24: a 1-lot ask
	// filled as 0.95 + 0.05). Callers that book whole contracts accumulate
	// CountFP per order and book as wholes complete.
	Count     int
	CountFP   float64
	YesPriceC int
	FeeC      int
	Time      time.Time
	// Malformed is non-empty when the exchange reported the fill in a shape
	// that cannot be booked at all (missing count, unusable price). Such a
	// fill still flows to the caller — erroring the whole poll would wedge
	// the fills cursor on one poison fill forever (observed live
	// 2026-07-24) — but its numbers are not trustworthy and the caller must
	// treat the account as diverged.
	Malformed string
}

type fillsResp struct {
	Fills []struct {
		FillID      string `json:"fill_id"`
		TradeID     string `json:"trade_id"`
		OrderID     string `json:"order_id"`
		Ticker      string `json:"ticker"`
		BookSide    string `json:"book_side"`
		IsTaker     bool   `json:"is_taker"`
		CountFP     string `json:"count_fp"`
		YesPriceD   string `json:"yes_price_dollars"`
		FeeCost     string `json:"fee_cost"`
		CreatedTime string `json:"created_time"`
	} `json:"fills"`
	Cursor string `json:"cursor"`
}

// Fills returns portfolio fills at or after since, oldest first, cursor-
// paged. min_ts is second-granular and treated inclusively; callers dedupe
// by FillID across polls (a restart's re-read makes duplicates normal).
func (c *Client) Fills(ctx context.Context, since time.Time) ([]Fill, error) {
	if c.signer == nil {
		return nil, fmt.Errorf("kalshi: Fills requires an authenticated client")
	}
	var out []Fill
	cursor := ""
	for {
		path := fmt.Sprintf("/portfolio/fills?limit=100&min_ts=%d", since.Unix())
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var fr fillsResp
		if err := c.get(ctx, path, "fills", &fr); err != nil {
			return nil, err
		}
		for _, f := range fr.Fills {
			var malformed string
			countFP := parseDollars(f.CountFP) // fixed-point count, fractional allowed
			if f.CountFP == "" || countFP <= 0 {
				malformed = fmt.Sprintf("count %q unusable", f.CountFP)
			}
			count, err := parseFixedCount(f.CountFP)
			if err != nil {
				count = 0 // fractional piece: CountFP carries the truth
			}
			priceC := dollarsToCents(f.YesPriceD)
			if malformed == "" && (priceC < 1 || priceC > 99) {
				// Fill prices book into a real-money ledger: strict, like
				// CreateOrder's average_fill_price — but flagged per-fill,
				// never a poll-wedging page error.
				malformed = fmt.Sprintf("yes_price %q unusable", f.YesPriceD)
			}
			t, _ := time.Parse(time.RFC3339, f.CreatedTime)
			id := f.FillID
			if id == "" {
				id = f.TradeID
			}
			out = append(out, Fill{
				FillID: id, OrderID: f.OrderID, Ticker: f.Ticker, BookSide: f.BookSide,
				IsTaker: f.IsTaker, Count: count, CountFP: countFP, YesPriceC: priceC,
				// Fees stay lenient BY CHOICE (same reasoning as CreateOrder):
				// an absent fee undercounts P&L by cents; erroring would leave
				// a real fill unbooked over a cosmetic field.
				FeeC: int(parseDollars(f.FeeCost)*100 + 0.5),
				Time: t, Malformed: malformed,
			})
		}
		if fr.Cursor == "" || len(fr.Fills) == 0 {
			break
		}
		cursor = fr.Cursor
	}
	// Oldest first regardless of page order.
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}
