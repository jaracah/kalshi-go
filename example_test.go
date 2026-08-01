package kalshi_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	kalshi "github.com/jaracah/kalshi-go"
)

// Read public market data with no credentials.
func ExampleNewClient() {
	c := kalshi.NewClient(nil)

	yesBid, yesAsk, err := c.FetchOrderbook(context.Background(), "KXBTCD-26JUL1612-T110000")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("yes %d¢ bid / %d¢ ask\n", yesBid, yesAsk)
}

// Place a limit order with an authenticated client.
func ExampleClient_CreateOrder() {
	signer, err := kalshi.NewSigner(
		os.Getenv("KALSHI_KEY_ID"),
		os.Getenv("KALSHI_PRIVATE_KEY"), // PEM, PKCS#1 or PKCS#8
	)
	if err != nil {
		log.Fatal(err)
	}
	c := kalshi.NewAuthedClient(nil, signer, "") // "" = production

	res, err := c.CreateOrder(context.Background(), kalshi.Order{
		Ticker: "KXBTCD-26JUL1612-T110000",
		Side:   kalshi.SideBid, // buy YES
		Count:  10,
		PriceC: 55, // 55¢ limit; zero TimeInForce = immediate-or-cancel
		// Required: it makes retrying a 429 or ambiguous transport failure
		// safe — the exchange dedupes on it. Unique per logical order.
		ClientOrderID: "d5f4f4a2-1f5e-4c3a-9b6d-8f2a7c1e0b42",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("order %s: filled %d @ %d¢, %d resting\n",
		res.OrderID, res.FillCount, res.AvgPriceC, res.Remaining)
}

// Rest a post-only maker quote, then discover its fills asynchronously.
func ExampleClient_Fills() {
	signer, err := kalshi.NewSigner(
		os.Getenv("KALSHI_KEY_ID"),
		os.Getenv("KALSHI_PRIVATE_KEY"),
	)
	if err != nil {
		log.Fatal(err)
	}
	c := kalshi.NewAuthedClient(nil, signer, "")
	ctx := context.Background()

	res, err := c.CreateOrder(ctx, kalshi.Order{
		Ticker:        "KXBTCD-26JUL1612-T110000",
		Side:          kalshi.SideAsk, // sell YES == buy NO at 100−price
		Count:         5,
		PriceC:        95,
		TimeInForce:   kalshi.TIFGoodTillCanceled,
		PostOnly:      true, // reject instead of crossing
		ClientOrderID: "0b7c9d1e-2f3a-4b5c-8d9e-6f1a2b3c4d5e",
	})
	if errors.Is(err, kalshi.ErrPostOnlyCross) {
		return // would have taken; the quote simply does not rest
	} else if err != nil {
		log.Fatal(err)
	}

	fills, err := c.Fills(ctx, time.Now().Add(-time.Minute))
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range fills {
		if f.OrderID != res.OrderID {
			continue
		}
		if f.Malformed != "" {
			log.Printf("unbookable fill %s: %s", f.FillID, f.Malformed)
			continue
		}
		// CountFP can be fractional even though the order was whole:
		// counterparties trade fractional contracts.
		fmt.Printf("filled %.2f @ %d¢ (fee %d¢)\n", f.CountFP, f.YesPriceC, f.FeeC)
	}
}
