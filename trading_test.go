package kalshi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newAuthedTestClient wires an authed client to a stub exchange.
func newAuthedTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s, err := NewSigner("test-key", testKeyPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	return NewAuthedClient(srv.Client(), s, srv.URL)
}

func TestCreateOrderWireShape(t *testing.T) {
	var got createOrderV2Request
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/portfolio/events/orders" {
			t.Errorf("hit %s %s, want POST /portfolio/events/orders", r.Method, r.URL.Path)
		}
		for _, h := range []string{"KALSHI-ACCESS-KEY", "KALSHI-ACCESS-SIGNATURE", "KALSHI-ACCESS-TIMESTAMP"} {
			if r.Header.Get(h) == "" {
				t.Errorf("missing auth header %s", h)
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"order_id":"o1","client_order_id":"cid-1","fill_count":"3.00","remaining_count":"7.00","average_fill_price":"0.0500","average_fee_paid":"0.003500","ts_ms":1789000000000}`)
	})

	res, err := c.CreateOrder(context.Background(), Order{
		Ticker: "KXBTCD-26JUL1612-T110000", Side: SideAsk, Count: 10, PriceC: 5,
		ClientOrderID: "cid-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := createOrderV2Request{
		Ticker: "KXBTCD-26JUL1612-T110000", ClientOrderID: "cid-1", Side: "ask",
		Count: "10", Price: "0.05",
		TimeInForce: "immediate_or_cancel", SelfTradePreventionType: "taker_at_cross",
	}
	if got != want {
		t.Errorf("wire request = %+v, want %+v", got, want)
	}
	if res.FillCount != 3 || res.Remaining != 7 || res.AvgPriceC != 5 {
		t.Errorf("result = %+v, want fill 3 remaining 7 avg 5¢", res)
	}
	if res.TotalFeeC != 1 { // 3 × $0.0035 = $0.0105 → 1¢
		t.Errorf("TotalFeeC = %d, want 1", res.TotalFeeC)
	}
}

func TestCreateOrderValidation(t *testing.T) {
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid orders must not reach the exchange")
	})
	cases := []Order{
		{Ticker: "T", Side: "yes", Count: 1, PriceC: 50, ClientOrderID: "x"}, // legacy side vocab
		{Ticker: "T", Side: SideBid, Count: 0, PriceC: 50, ClientOrderID: "x"},
		{Ticker: "T", Side: SideBid, Count: 1, PriceC: 0, ClientOrderID: "x"},
		{Ticker: "T", Side: SideBid, Count: 1, PriceC: 100, ClientOrderID: "x"},
		{Ticker: "T", Side: SideBid, Count: 1, PriceC: 50}, // no idempotency id
	}
	for _, o := range cases {
		if _, err := c.CreateOrder(context.Background(), o); err == nil {
			t.Errorf("order %+v should be rejected client-side", o)
		}
	}
	// Unauthed client refuses outright.
	if _, err := NewClient(nil).CreateOrder(context.Background(), cases[0]); err == nil {
		t.Error("read-only client must refuse CreateOrder")
	}
}

func TestPositionsPaginationAndSigns(t *testing.T) {
	page := 0
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if page++; page == 1 {
			// A cursor with +, / and = — it must round-trip URL-escaped.
			fmt.Fprint(w, `{"cursor":"a+b/c==","market_positions":[{"ticker":"A","position_fp":"5.00","market_exposure_dollars":"2.50"}]}`)
			return
		}
		if got := r.URL.Query().Get("cursor"); got != "a+b/c==" {
			t.Errorf("page-2 cursor = %q, want %q (escaping lost it)", got, "a+b/c==")
		}
		fmt.Fprint(w, `{"cursor":"","market_positions":[{"ticker":"B","position_fp":"-10.00","market_exposure_dollars":"9.70"}]}`)
	})
	ps, err := c.Positions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].NetYes != 5 || ps[1].NetYes != -10 {
		t.Fatalf("positions = %+v, want A +5 / B -10", ps)
	}
	if ps[0].ExposureC != 250 || ps[1].ExposureC != 970 {
		t.Errorf("exposures = %d,%d want 250,970", ps[0].ExposureC, ps[1].ExposureC)
	}
}

func TestFixedPointHelpers(t *testing.T) {
	for c, want := range map[int]string{5: "0.05", 56: "0.56", 100: "1.00", 99: "0.99"} {
		if got := centsToDollars(c); got != want {
			t.Errorf("centsToDollars(%d) = %q, want %q", c, got, want)
		}
	}
	if n, err := parseFixedCount("10.00"); err != nil || n != 10 {
		t.Errorf(`parseFixedCount("10.00") = %d, %v`, n, err)
	}
	if n, err := parseFixedCount("-3.00"); err != nil || n != -3 {
		t.Errorf(`parseFixedCount("-3.00") = %d, %v`, n, err)
	}
	if _, err := parseFixedCount("2.50"); err == nil {
		t.Error("fractional contracts must error, not round")
	}
	if _, err := parseFixedCount(""); err == nil {
		t.Error("empty count is a missing required field, not zero")
	}
}

func TestCreateOrderRefusesUnusableFillPrice(t *testing.T) {
	c := newAuthedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		// Filled, but the price field is garbage: booking this at 0¢ would
		// corrupt a real-money ledger — it must error instead.
		fmt.Fprint(w, `{"order_id":"o1","fill_count":"3.00","remaining_count":"0.00","average_fill_price":"","average_fee_paid":"0.001","ts_ms":1}`)
	})
	if _, err := c.CreateOrder(context.Background(), Order{
		Ticker: "T", Side: SideAsk, Count: 3, PriceC: 5, ClientOrderID: "x",
	}); err == nil {
		t.Error("filled order with unusable average_fill_price must error")
	}
}
