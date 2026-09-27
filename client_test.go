package kalshi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDollarsToCents(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"0.0000": 0,
		"0.2400": 24,
		"0.235":  24, // 23.5 -> nearest cent
		"0.999":  100,
		"1.0000": 100,
		"junk":   0,
	}
	for in, want := range cases {
		if got := dollarsToCents(in); got != want {
			t.Errorf("dollarsToCents(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestBestLevelCents(t *testing.T) {
	if got := bestLevelCents(nil); got != 0 {
		t.Errorf("empty book = %d, want 0", got)
	}
	levels := [][]string{
		{"0.0100", "5"},
		{"0.4500", "10"}, // highest
		{"0.2300", "3"},
		{"bad"}, // malformed -> skipped
		{},      // empty -> skipped
	}
	if got := bestLevelCents(levels); got != 45 {
		t.Errorf("best level = %d, want 45", got)
	}
}

func TestPickLive(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	rfc := func(tm time.Time) string { return tm.Format(time.RFC3339) }
	buffer := 30 * time.Second

	markets := []Market{
		// soonest close, but inside the 30s buffer -> skipped
		{Ticker: "EXPIRING", OpenTime: rfc(now.Add(-10 * time.Minute)), CloseTime: rfc(now.Add(20 * time.Second))},
		// open and well within its window -> the pick
		{Ticker: "LIVE", OpenTime: rfc(now.Add(-5 * time.Minute)), CloseTime: rfc(now.Add(10 * time.Minute))},
		// not opened yet -> skipped
		{Ticker: "FUTURE", OpenTime: rfc(now.Add(5 * time.Minute)), CloseTime: rfc(now.Add(20 * time.Minute))},
	}
	m, ok := PickLive(markets, now, buffer)
	if !ok || m.Ticker != "LIVE" {
		t.Fatalf("PickLive = (%q, %v), want (LIVE, true)", m.Ticker, ok)
	}
}

func TestPickLive_NoneLive(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	markets := []Market{
		{Ticker: "PAST", CloseTime: now.Add(-time.Minute).Format(time.RFC3339)},
		{Ticker: "BADTIME", CloseTime: "not-a-time"},
	}
	if _, ok := PickLive(markets, now, 30*time.Second); ok {
		t.Error("expected no live market")
	}
}

// Fixture per the get-exchange-status OpenAPI docs (verified 2026-08-02).
func TestFetchExchangeStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exchange/status" {
			t.Errorf("path = %q, want /exchange/status", r.URL.Path)
		}
		fmt.Fprint(w, `{"exchange_active":true,"trading_active":false,"exchange_estimated_resume_time":"2026-08-02T14:00:00Z"}`)
	}))
	defer srv.Close()

	c := NewAuthedClient(srv.Client(), nil, srv.URL)
	st, err := c.FetchExchangeStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.ExchangeActive || st.TradingActive || st.EstimatedResumeTime != "2026-08-02T14:00:00Z" {
		t.Errorf("status = %+v, want active exchange, halted trading, resume time", st)
	}
}

func TestFetchOrderbookDepth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"orderbook_fp":{
			"yes_dollars":[["0.03","1200"],["0.65","40"],["bad","1"],["0.10"]],
			"no_dollars":[["0.90","7"],["0.97","104"]]}}`)
	}))
	defer srv.Close()

	c := NewAuthedClient(srv.Client(), nil, srv.URL)
	yes, no, err := c.FetchOrderbookDepth(context.Background(), "KXBTCD-26JUL2212-T109000")
	if err != nil {
		t.Fatal(err)
	}
	// Best-priced first; malformed levels dropped.
	if len(yes) != 2 || yes[0] != (Level{PriceC: 65, Size: 40}) || yes[1] != (Level{PriceC: 3, Size: 1200}) {
		t.Errorf("yes = %+v", yes)
	}
	if len(no) != 2 || no[0] != (Level{PriceC: 97, Size: 104}) || no[1] != (Level{PriceC: 90, Size: 7}) {
		t.Errorf("no = %+v", no)
	}
}
