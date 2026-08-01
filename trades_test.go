package kalshi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchTrades_PagesAndNormalizes(t *testing.T) {
	const cursor = "pg+2/=" // exercises the QueryEscape path: unescaped, + arrives as a space
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch got := r.URL.Query().Get("cursor"); got {
		case "":
			fmt.Fprintf(w, `{"cursor":%q,"trades":[
				{"created_time":"2026-05-12T07:53:32.458297Z","taker_side":"yes","count_fp":"935.19","yes_price_dollars":"0.0100"},
				{"created_time":"2026-05-11T10:00:00Z","taker_side":"no","count_fp":"5","yes_price_dollars":"0.9700"}]}`, cursor)
		case cursor:
			fmt.Fprint(w, `{"cursor":"","trades":[
				{"created_time":"2026-05-11T09:00:00Z","taker_side":"no","count_fp":"12.5","yes_price_dollars":"0.4500"}]}`)
		default:
			t.Errorf("unexpected cursor %q", got)
			fmt.Fprint(w, `{"cursor":"","trades":[]}`)
		}
	}))
	defer srv.Close()

	c := NewAuthedClient(srv.Client(), nil, srv.URL)
	trades, err := c.FetchTrades(context.Background(), "KXBTCD-26MAY1112-T104000")
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 3 {
		t.Fatalf("got %d trades, want 3", len(trades))
	}
	// Oldest first regardless of page order.
	if trades[0].YesCents != 45 || trades[0].Count != 12.5 || trades[0].TakerSide != "no" {
		t.Errorf("first trade = %+v, want the 09:00 45c x12.5 no-taker", trades[0])
	}
	if trades[2].CreatedTime != "2026-05-12T07:53:32.458297Z" || trades[2].Count != 935.19 || trades[2].YesCents != 1 {
		t.Errorf("last trade = %+v, want the fractional-count 1c print", trades[2])
	}
	for i := 1; i < len(trades); i++ {
		if trades[i].Time.Before(trades[i-1].Time) {
			t.Fatalf("trades not sorted ascending at %d", i)
		}
	}
}

func TestFetchTradesSince_StrictBoundaryAndPaging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("min_ts"); got != "1747044000" { // 2025-05-12T10:00:00Z
			t.Errorf("min_ts = %q, want 1747044000", got)
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"cursor":"p2","trades":[
				{"trade_id":"c","created_time":"2025-05-12T10:00:02Z","taker_side":"yes","count_fp":"3","yes_price_dollars":"0.0800"},
				{"trade_id":"a","created_time":"2025-05-12T10:00:00Z","taker_side":"yes","count_fp":"1","yes_price_dollars":"0.0500"}]}`)
		default:
			fmt.Fprint(w, `{"cursor":"","trades":[
				{"trade_id":"b","created_time":"2025-05-12T10:00:01Z","taker_side":"no","count_fp":"2","yes_price_dollars":"0.9200"}]}`)
		}
	}))
	defer srv.Close()

	c := NewAuthedClient(srv.Client(), nil, srv.URL)
	since := time.Date(2025, 5, 12, 10, 0, 0, 0, time.UTC)
	trades, err := c.FetchTradesSince(context.Background(), "KXBTCD-25MAY1212-T103000", since)
	if err != nil {
		t.Fatal(err)
	}
	// Trade "a" sits exactly AT since: min_ts is second-granular, the strict
	// filter must drop it. b and c survive, oldest first.
	if len(trades) != 2 || trades[0].TradeID != "b" || trades[1].TradeID != "c" {
		t.Fatalf("trades = %+v, want [b, c]", trades)
	}
	if trades[0].YesCents != 92 || trades[1].Count != 3 {
		t.Errorf("normalize missing: %+v", trades)
	}
}
