package kalshi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestCreateOrderGTCPostOnlyWireShape(t *testing.T) {
	var got createOrderV2Request
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"order_id":"o2","fill_count":"0.00","remaining_count":"1.00","ts_ms":1789000000000}`)
	})

	// A NO bid at 95¢ rests as a YES ask at 5¢ — the maker's shape.
	res, err := c.CreateOrder(context.Background(), Order{
		Ticker: "KXBTCD-26JUL2312-T110500", Side: SideAsk, Count: 1, PriceC: 5,
		TimeInForce: TIFGoodTillCanceled, PostOnly: true, ClientOrderID: "cid-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TimeInForce != "good_till_canceled" || !got.PostOnly {
		t.Errorf("wire tif/post_only = %q/%v, want good_till_canceled/true", got.TimeInForce, got.PostOnly)
	}
	if res.FillCount != 0 || res.Remaining != 1 {
		t.Errorf("result = %+v, want resting 1", res)
	}

	// post_only without a resting TIF is a caller bug, refused client-side.
	if _, err := c.CreateOrder(context.Background(), Order{
		Ticker: "T", Side: SideAsk, Count: 1, PriceC: 5, PostOnly: true, ClientOrderID: "x",
	}); err == nil {
		t.Error("post_only IOC should be refused")
	}
}

func TestCancelOrder(t *testing.T) {
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/portfolio/events/orders/o-77" {
			t.Errorf("hit %s %s, want DELETE /portfolio/events/orders/o-77", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"order_id":"o-77","client_order_id":"cid-77","reduced_by":"1.00","ts_ms":1789000000001}`)
	})
	res, err := c.CancelOrder(context.Background(), "o-77")
	if err != nil {
		t.Fatal(err)
	}
	if res.ReducedBy != 1 || res.OrderID != "o-77" {
		t.Errorf("cancel result = %+v", res)
	}
}

func TestCancelOrderGoneIsTyped(t *testing.T) {
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"not_found"}}`, http.StatusNotFound)
	})
	_, err := c.CancelOrder(context.Background(), "o-gone")
	if !errors.Is(err, ErrOrderNotFound) {
		t.Errorf("err = %v, want ErrOrderNotFound", err)
	}
}

func TestOpenOrdersPaging(t *testing.T) {
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("status"); got != "resting" {
			t.Errorf("status = %q, want resting", got)
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"cursor":"p2","orders":[
				{"order_id":"o1","client_order_id":"c1","ticker":"KXBTCD-26JUL2312-T110500",
				 "book_side":"ask","yes_price_dollars":"0.0500","fill_count_fp":"0.00",
				 "remaining_count_fp":"1.00","created_time":"2026-07-23T00:01:00Z"}]}`)
		default:
			fmt.Fprint(w, `{"cursor":"","orders":[
				{"order_id":"o2","client_order_id":"c2","ticker":"KXBTCD-26JUL2314-T111000",
				 "book_side":"ask","yes_price_dollars":"0.1000","fill_count_fp":"1.00",
				 "remaining_count_fp":"2.00","created_time":"2026-07-23T00:02:00Z"}]}`)
		}
	})
	orders, err := c.OpenOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 2 {
		t.Fatalf("orders = %d, want 2", len(orders))
	}
	if o := orders[0]; o.OrderID != "o1" || o.BookSide != SideAsk || o.YesPriceC != 5 || o.Remaining != 1 || o.Filled != 0 {
		t.Errorf("o1 = %+v", o)
	}
	if o := orders[1]; o.YesPriceC != 10 || o.Remaining != 2 || o.Filled != 1 {
		t.Errorf("o2 = %+v", o)
	}
}

// The 2026-07-27 live incident, at the wire. A 5-lot ask part-filled by a
// fractional counterparty rests at "4.82"; rejecting that failed the whole
// OpenOrders call, which blinded reconciliation and broke cancel-all — the
// emergency stop — for as long as the order rested. Fractional counts are
// now data, and an order nobody can parse degrades alone instead of taking
// the page with it.
func TestOpenOrdersFractionalCounts(t *testing.T) {
	c := newAuthedTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"cursor":"","orders":[
			{"order_id":"c56c21b8","ticker":"KXBTCD-26JUL2716-T108000","book_side":"ask",
			 "yes_price_dollars":"0.0600","fill_count_fp":"0.18","remaining_count_fp":"4.82",
			 "created_time":"2026-07-27T00:00:00Z"},
			{"order_id":"later","ticker":"T2","book_side":"ask",
			 "yes_price_dollars":"0.0600","fill_count_fp":"1.18","remaining_count_fp":"3.82",
			 "created_time":"2026-07-27T00:00:00Z"},
			{"order_id":"bad","ticker":"T3","book_side":"ask",
			 "yes_price_dollars":"0.0600","fill_count_fp":"0.00","remaining_count_fp":"garbage",
			 "created_time":"2026-07-27T00:00:00Z"}]}`)
	})
	orders, err := c.OpenOrders(context.Background())
	if err != nil {
		t.Fatalf("one unparseable order must not fail the page: %v", err)
	}
	if len(orders) != 3 {
		t.Fatalf("orders = %d, want 3", len(orders))
	}
	// 4.82 resting on a 5-lot: 5 wholes can still book (the 0.18 is part of
	// the first), 0 have booked so far.
	if o := orders[0]; o.Remaining != 5 || o.Filled != 0 || o.RemainingFP != 4.82 || o.FilledFP != 0.18 {
		t.Errorf("part-filled 5-lot = %+v; want Remaining 5, Filled 0, FP 4.82/0.18", o)
	}
	// After the first whole completes: 4 still to book, 1 booked.
	if o := orders[1]; o.Remaining != 4 || o.Filled != 1 {
		t.Errorf("3.82 resting = %+v; want Remaining 4, Filled 1", o)
	}
	if o := orders[2]; o.Malformed == "" || o.Remaining != 0 {
		t.Errorf("unparseable order must be flagged, not trusted: %+v", o)
	}
	if o := orders[2]; o.OrderID != "bad" {
		t.Errorf("an unparseable order must still carry the id that can cancel it: %+v", o)
	}
}

// A cancel that lands after a fractional piece filled reports a fractional
// reduced_by. It must parse, and the sub-contract gap must still be visible
// to the caller so the fill gets discovered.
func TestCancelOrderFractionalReducedBy(t *testing.T) {
	c := newAuthedTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"order_id":"o1","client_order_id":"c1","reduced_by":"4.82","ts_ms":2}`)
	})
	res, err := c.CancelOrder(context.Background(), "o1")
	if err != nil {
		t.Fatalf("fractional reduced_by must parse: %v", err)
	}
	if res.ReducedBy != 5 || res.ReducedByFP != 4.82 {
		t.Errorf("reduced_by = %d / %v, want 5 / 4.82", res.ReducedBy, res.ReducedByFP)
	}
}

func TestFillsPagingStrictPriceLenientFee(t *testing.T) {
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("min_ts"); got != "1784851200" {
			t.Errorf("min_ts = %q, want 1784851200", got)
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"cursor":"p2","fills":[
				{"fill_id":"f2","order_id":"o1","ticker":"T1","book_side":"ask","is_taker":false,
				 "count_fp":"1.00","yes_price_dollars":"0.0500","fee_cost":"","created_time":"2026-07-23T01:00:00Z"}]}`)
		default:
			fmt.Fprint(w, `{"cursor":"","fills":[
				{"fill_id":"f1","order_id":"o1","ticker":"T1","book_side":"ask","is_taker":false,
				 "count_fp":"1.00","yes_price_dollars":"0.0500","fee_cost":"0.010000","created_time":"2026-07-23T00:30:00Z"}]}`)
		}
	})
	since := time.Unix(1784851200, 0).UTC()
	fills, err := c.Fills(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 2 || fills[0].FillID != "f1" || fills[1].FillID != "f2" {
		t.Fatalf("fills = %+v, want [f1, f2] oldest first", fills)
	}
	if fills[0].FeeC != 1 || fills[1].FeeC != 0 {
		t.Errorf("fees = %d/%d, want 1/0 (empty fee is lenient)", fills[0].FeeC, fills[1].FeeC)
	}
	if fills[0].YesPriceC != 5 || fills[0].IsTaker {
		t.Errorf("f1 = %+v", fills[0])
	}
}

func TestFillsFlagsMalformedPerFill(t *testing.T) {
	// Unusable prices and fractional counts flag the FILL, never error the
	// page: a page error re-reads the same poison fill every poll forever
	// and no later fill can book (observed live 2026-07-24).
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"cursor":"","fills":[
			{"fill_id":"f1","order_id":"o1","ticker":"T1","book_side":"ask","is_taker":false,
			 "count_fp":"1.00","yes_price_dollars":"","fee_cost":"0","created_time":"2026-07-23T00:30:00Z"},
			{"fill_id":"f2","order_id":"o2","ticker":"T2","book_side":"ask","is_taker":false,
			 "count_fp":"0.95","yes_price_dollars":"0.0500","fee_cost":"0","created_time":"2026-07-23T00:30:01Z"},
			{"fill_id":"f3","order_id":"o3","ticker":"T3","book_side":"ask","is_taker":false,
			 "count_fp":"1.00","yes_price_dollars":"0.0500","fee_cost":"0","created_time":"2026-07-23T00:30:02Z"}]}`)
	})
	fills, err := c.Fills(context.Background(), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("per-fill data issues must not error the page: %v", err)
	}
	if len(fills) != 3 {
		t.Fatalf("got %d fills, want 3", len(fills))
	}
	if fills[0].Malformed == "" {
		t.Error("empty yes_price must flag the fill malformed")
	}
	if fills[1].Malformed != "" {
		t.Errorf("fractional count_fp is a piece, not malformed: %q", fills[1].Malformed)
	}
	if fills[1].Count != 0 || fills[1].CountFP != 0.95 {
		t.Errorf("fractional piece: Count=%d CountFP=%v, want 0 and 0.95", fills[1].Count, fills[1].CountFP)
	}
	if fills[2].Malformed != "" {
		t.Errorf("clean fill wrongly flagged: %q", fills[2].Malformed)
	}
}
