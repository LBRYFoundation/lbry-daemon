package exchangerate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	rateMaxAge       = 10 * time.Minute
	feedUpdate       = 5 * time.Minute
	feedOnlineWindow = feedUpdate + 50*time.Second
)

var ErrCurrencyConversion = errors.New("currency conversion is unavailable")

type Rate struct {
	Market    string
	Spot      *big.Rat
	Timestamp time.Time
	LastCheck time.Time
}

type feed struct {
	name, market, url string
	fee               float64
	decode            func(any) (float64, error)
}

type Manager struct {
	mu     sync.RWMutex
	rates  map[string]Rate
	feeds  []feed
	client *http.Client
	now    func() time.Time
}

func New() *Manager {
	return &Manager{rates: make(map[string]Rate), now: time.Now}
}

func NewProduction(client *http.Client) *Manager {
	manager := New()
	if client == nil {
		client = &http.Client{Timeout: 50 * time.Second}
	}
	manager.client = client
	manager.feeds = []feed{
		{name: "Bittrex BTC", market: "BTCLBC", url: "https://api.bittrex.com/v3/markets/LBC-BTC/ticker", fee: 0.0025, decode: bittrexRate},
		{name: "Bittrex USD", market: "USDLBC", url: "https://api.bittrex.com/v3/markets/LBC-USD/ticker", fee: 0.0025, decode: bittrexRate},
		{name: "CoinEx BTC", market: "BTCLBC", url: "https://api.coinex.com/v1/market/ticker?market=LBCBTC", decode: coinExRate},
		{name: "CoinEx USD", market: "USDLBC", url: "https://api.coinex.com/v1/market/ticker?market=LBCUSDT", decode: coinExRate},
	}
	return manager
}

func (manager *Manager) Start(ctx context.Context) {
	if manager == nil {
		return
	}
	for _, configured := range manager.feeds {
		configured := configured
		go func() {
			manager.refresh(ctx, configured)
			ticker := time.NewTicker(feedUpdate)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					manager.refresh(ctx, configured)
				}
			}
		}()
	}
}

func (manager *Manager) SetRate(name, market, spot string, timestamp, lastCheck time.Time) error {
	spotFloat, err := strconv.ParseFloat(spot, 64)
	if err != nil || spotFloat <= 0 {
		return errors.New("Spot must be greater than 0.")
	}
	value := new(big.Rat).SetFloat64(spotFloat)
	if manager.now().Sub(timestamp) >= rateMaxAge {
		return errors.New("The timestamp is too dated.")
	}
	manager.mu.Lock()
	manager.rates[name] = Rate{Market: market, Spot: value, Timestamp: timestamp, LastCheck: lastCheck}
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) ConvertCurrency(from, to, amount string) (string, error) {
	value, ok := new(big.Rat).SetString(amount)
	if !ok {
		return "", fmt.Errorf("invalid amount %q", amount)
	}
	if from == to {
		return roundDecimal(value, 8), nil
	}
	now := manager.now()
	manager.mu.RLock()
	spots := make([]*big.Rat, 0, len(manager.rates))
	for _, rate := range manager.rates {
		if rate.Market == from+to && now.Before(rate.LastCheck.Add(feedOnlineWindow)) {
			spots = append(spots, new(big.Rat).Set(rate.Spot))
		}
	}
	manager.mu.RUnlock()
	if len(spots) == 0 {
		return "", fmt.Errorf("%w: Unable to convert %s from %s to %s", ErrCurrencyConversion, amount, from, to)
	}
	sort.Slice(spots, func(i, j int) bool { return spots[i].Cmp(spots[j]) < 0 })
	median := spots[len(spots)/2]
	if len(spots)%2 == 0 {
		median = new(big.Rat).Quo(new(big.Rat).Add(spots[len(spots)/2-1], median), big.NewRat(2, 1))
	}
	return roundDecimal(new(big.Rat).Mul(value, median), 8), nil
}

func (manager *Manager) ToDewies(currency, amount string) (uint64, error) {
	converted, err := manager.ConvertCurrency(currency, "LBC", amount)
	if err != nil {
		return 0, err
	}
	value, _ := new(big.Rat).SetString(converted)
	value.Mul(value, big.NewRat(100_000_000, 1))
	if !value.IsInt() || value.Sign() < 0 || !value.Num().IsUint64() {
		return 0, fmt.Errorf("invalid converted LBC amount %q", converted)
	}
	return value.Num().Uint64(), nil
}

func (manager *Manager) refresh(ctx context.Context, configured feed) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configured.url, nil)
	if err != nil {
		return
	}
	request.Header.Set("User-Agent", "lbrynet")
	response, err := manager.client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	var payload any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return
	}
	spot, err := configured.decode(payload)
	if err != nil || spot <= 0 {
		return
	}
	spot = 1.0 / spot
	if configured.fee != 0 {
		spot /= 1.0 - configured.fee
	}
	now := manager.now()
	_ = manager.SetRate(configured.name, configured.market, strconv.FormatFloat(spot, 'g', -1, 64), now, now)
}

func bittrexRate(payload any) (float64, error) {
	object, ok := payload.(map[string]any)
	if !ok {
		return 0, errors.New("Bittrex result not found")
	}
	return jsonNumberRat(object["lastTradeRate"])
}

func coinExRate(payload any) (float64, error) {
	object, ok := payload.(map[string]any)
	if !ok {
		return 0, errors.New("CoinEx result not found")
	}
	data, _ := object["data"].(map[string]any)
	ticker, _ := data["ticker"].(map[string]any)
	return jsonNumberRat(ticker["last"])
}

func jsonNumberRat(value any) (float64, error) {
	text := fmt.Sprint(value)
	if number, ok := value.(float64); ok {
		text = strconv.FormatFloat(number, 'g', -1, 64)
	}
	rate, err := strconv.ParseFloat(text, 64)
	if err != nil || rate <= 0 {
		return 0, errors.New("invalid exchange rate response")
	}
	return rate, nil
}

func roundDecimal(value *big.Rat, places int) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(scale))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled.Num(), scaled.Denom(), remainder)
	twiceRemainder := new(big.Int).Lsh(new(big.Int).Abs(remainder), 1)
	comparison := twiceRemainder.Cmp(scaled.Denom())
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		if scaled.Sign() >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		} else {
			quotient.Sub(quotient, big.NewInt(1))
		}
	}
	return new(big.Rat).SetFrac(quotient, scale).FloatString(places)
}
