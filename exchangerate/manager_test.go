package exchangerate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConvertCurrencyUsesOnlineMedianAndPinnedRounding(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := New()
	manager.now = func() time.Time { return now }
	for _, rate := range []struct{ name, spot string }{{"one", "2"}, {"two", "4"}, {"three", "100"}} {
		if err := manager.SetRate(rate.name, "USDLBC", rate.spot, now, now); err != nil {
			t.Fatal(err)
		}
	}
	converted, err := manager.ConvertCurrency("USD", "LBC", "1.234567891")
	if err != nil || converted != "4.93827156" {
		t.Fatalf("conversion = %q, %v", converted, err)
	}
	dewies, err := manager.ToDewies("USD", "1.234567891")
	if err != nil || dewies != 493_827_156 {
		t.Fatalf("dewies = %d, %v", dewies, err)
	}
	if same, err := manager.ConvertCurrency("LBC", "LBC", "1.000000005"); err != nil || same != "1.00000000" {
		t.Fatalf("half-even conversion = %q, %v", same, err)
	}
}

func TestRefreshAppliesPinnedInverseAndFeedFee(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"lastTradeRate":"0.5"}`))
	}))
	defer server.Close()
	now := time.Unix(1_700_000_000, 0)
	manager := New()
	manager.client = server.Client()
	manager.now = func() time.Time { return now }
	manager.refresh(context.Background(), feed{
		name: "Bittrex", market: "USDLBC", url: server.URL, fee: 0.0025, decode: bittrexRate,
	})
	converted, err := manager.ConvertCurrency("USD", "LBC", "1")
	if err != nil || converted != "2.00501253" {
		t.Fatalf("refreshed conversion = %q, %v", converted, err)
	}
}

func TestConvertCurrencyRejectsOfflineAndInvalidRates(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := New()
	manager.now = func() time.Time { return now }
	if err := manager.SetRate("old", "BTCLBC", "2", now, now.Add(-feedOnlineWindow)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ConvertCurrency("BTC", "LBC", "1"); !errors.Is(err, ErrCurrencyConversion) {
		t.Fatalf("offline conversion error = %v", err)
	}
	if err := manager.SetRate("zero", "BTCLBC", "0", now, now); err == nil {
		t.Fatal("zero spot accepted")
	}
	if err := manager.SetRate("dated", "BTCLBC", "1", now.Add(-rateMaxAge), now); err == nil {
		t.Fatal("dated rate accepted")
	}
}
