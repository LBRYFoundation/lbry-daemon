package wallet

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"time"
)

const (
	CoinSelectionMaximumTries = 100_000

	CoinSelectionStrategyStandard        = "standard"
	CoinSelectionStrategyPreferConfirmed = "prefer_confirmed"
	CoinSelectionStrategyOnlyConfirmed   = "only_confirmed"
	CoinSelectionStrategyBranchAndBound  = "branch_and_bound"
	CoinSelectionStrategyClosestMatch    = "closest_match"
	CoinSelectionStrategyRandomDraw      = "random_draw"
	// SQLite is implemented by ledgerdb before CoinSelector is invoked.
	CoinSelectionStrategySQLite = "sqlite"
)

var ErrUnknownCoinSelectionStrategy = errors.New("unknown coin selection strategy")

// CoinSelectionEstimator carries the additional values retained by Python's
// OutputEffectiveAmountEstimator but not needed by transaction balancing.
type CoinSelectionEstimator struct {
	TransactionSpendable
	Fee    int64
	Height int64
}

// CoinSelector ports lbry.wallet.coinselection.CoinSelector. ExactMatch and
// Tries deliberately remain cumulative when the selector is reused.
type CoinSelector struct {
	Target       int64
	CostOfChange int64
	ExactMatch   bool
	Tries        int

	random coinSelectionRandom
}

type coinSelectionRandom interface {
	Float64() float64
}

func NewCoinSelector(target, costOfChange int64) *CoinSelector {
	return &CoinSelector{
		Target:       target,
		CostOfChange: costOfChange,
		random:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// NewCoinSelectorWithSeed matches random.Random(integer_seed).
func NewCoinSelectorWithSeed(target, costOfChange, seed int64) *CoinSelector {
	return &CoinSelector{
		Target:       target,
		CostOfChange: costOfChange,
		random:       newCoinSelectionPythonRandomFromInt(seed),
	}
}

// NewCoinSelectorWithStringSeed matches the SDK's Random(seed) followed by
// seed(seed, version=1), including its legacy 64-bit string hash.
func NewCoinSelectorWithStringSeed(target, costOfChange int64, seed string) *CoinSelector {
	return &CoinSelector{
		Target: target, CostOfChange: costOfChange,
		random: newCoinSelectionPythonRandomFromString(seed),
	}
}

// Select preserves Python's early returns: an empty or insufficient pool does
// not attempt to resolve the requested strategy.
func (selector *CoinSelector) Select(
	estimators []CoinSelectionEstimator, strategy string,
) ([]CoinSelectionEstimator, error) {
	if len(estimators) == 0 {
		return []CoinSelectionEstimator{}, nil
	}
	available := coinSelectionAvailable(estimators)
	if selector.Target > available {
		return []CoinSelectionEstimator{}, nil
	}

	switch strategy {
	case "", CoinSelectionStrategyStandard:
		return selector.Standard(estimators, available), nil
	case CoinSelectionStrategyPreferConfirmed:
		return selector.PreferConfirmed(estimators, available), nil
	case CoinSelectionStrategyOnlyConfirmed:
		return selector.OnlyConfirmed(estimators, available), nil
	case CoinSelectionStrategyBranchAndBound:
		return selector.BranchAndBound(estimators, available), nil
	case CoinSelectionStrategyClosestMatch:
		return selector.ClosestMatch(estimators, available), nil
	case CoinSelectionStrategyRandomDraw:
		return selector.RandomDraw(estimators, available), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownCoinSelectionStrategy, strategy)
	}
}

func (selector *CoinSelector) PreferConfirmed(
	estimators []CoinSelectionEstimator, available int64,
) []CoinSelectionEstimator {
	if selection := selector.OnlyConfirmed(estimators, available); len(selection) > 0 {
		return selection
	}
	return selector.Standard(estimators, available)
}

func (selector *CoinSelector) OnlyConfirmed(
	estimators []CoinSelectionEstimator, _ int64,
) []CoinSelectionEstimator {
	confirmed := make([]CoinSelectionEstimator, 0, len(estimators))
	for _, estimator := range estimators {
		if estimator.Height > 0 {
			confirmed = append(confirmed, estimator)
		}
	}
	if len(confirmed) == 0 {
		return []CoinSelectionEstimator{}
	}
	confirmedAvailable := coinSelectionAvailable(confirmed)
	if selector.Target > confirmedAvailable {
		return []CoinSelectionEstimator{}
	}
	return selector.Standard(confirmed, confirmedAvailable)
}

func (selector *CoinSelector) Standard(
	estimators []CoinSelectionEstimator, available int64,
) []CoinSelectionEstimator {
	if selection := selector.BranchAndBound(estimators, available); len(selection) > 0 {
		return selection
	}
	if selection := selector.ClosestMatch(estimators, available); len(selection) > 0 {
		return selection
	}
	return selector.RandomDraw(estimators, available)
}

func (selector *CoinSelector) BranchAndBound(
	estimators []CoinSelectionEstimator, available int64,
) []CoinSelectionEstimator {
	// Python list.sort(reverse=True) is stable and mutates the caller's list.
	sort.SliceStable(estimators, func(left, right int) bool {
		return estimators[left].EffectiveAmount > estimators[right].EffectiveAmount
	})

	currentValue := int64(0)
	currentAvailableValue := available
	currentSelection := make([]bool, 0, len(estimators))
	bestWaste := selector.CostOfChange
	bestSelection := make([]bool, 0, len(estimators))

	for selector.Tries < CoinSelectionMaximumTries {
		selector.Tries++

		backtrack := currentValue+currentAvailableValue < selector.Target ||
			currentValue > selector.Target+selector.CostOfChange
		if !backtrack && currentValue >= selector.Target {
			newWaste := currentValue - selector.Target
			if newWaste <= bestWaste {
				bestWaste = newWaste
				bestSelection = append(bestSelection[:0], currentSelection...)
			}
			backtrack = true
		}

		if backtrack {
			for len(currentSelection) > 0 && !currentSelection[len(currentSelection)-1] {
				currentSelection = currentSelection[:len(currentSelection)-1]
				currentAvailableValue += estimators[len(currentSelection)].EffectiveAmount
			}
			if len(currentSelection) == 0 {
				break
			}

			index := len(currentSelection) - 1
			currentSelection[index] = false
			currentValue -= estimators[index].EffectiveAmount
			continue
		}

		index := len(currentSelection)
		estimator := estimators[index]
		currentAvailableValue -= estimator.EffectiveAmount
		if index > 0 && !currentSelection[index-1] {
			previous := estimators[index-1]
			if estimator.EffectiveAmount == previous.EffectiveAmount && estimator.Fee == previous.Fee {
				currentSelection = append(currentSelection, false)
				continue
			}
		}
		currentSelection = append(currentSelection, true)
		currentValue += estimator.EffectiveAmount
	}

	if len(bestSelection) == 0 {
		return []CoinSelectionEstimator{}
	}
	selector.ExactMatch = true
	selection := make([]CoinSelectionEstimator, 0, len(bestSelection))
	for index, include := range bestSelection {
		if include {
			selection = append(selection, estimators[index])
		}
	}
	return selection
}

func (selector *CoinSelector) ClosestMatch(
	estimators []CoinSelectionEstimator, _ int64,
) []CoinSelectionEstimator {
	target := selector.Target + selector.CostOfChange
	smallestChange := int64(0)
	bestMatch := -1
	for index, estimator := range estimators {
		if estimator.EffectiveAmount < target {
			continue
		}
		change := estimator.EffectiveAmount - target
		if bestMatch == -1 || change < smallestChange {
			smallestChange = change
			bestMatch = index
		}
	}
	if bestMatch == -1 {
		return []CoinSelectionEstimator{}
	}
	return []CoinSelectionEstimator{estimators[bestMatch]}
}

func (selector *CoinSelector) RandomDraw(
	estimators []CoinSelectionEstimator, _ int64,
) []CoinSelectionEstimator {
	if selector.random == nil {
		selector.random = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	// The pinned runtime supplies random=self.random.random to shuffle. Its
	// historical implementation uses int(random() * (i+1)), not randbelow.
	for index := len(estimators) - 1; index > 0; index-- {
		swap := int(selector.random.Float64() * float64(index+1))
		estimators[index], estimators[swap] = estimators[swap], estimators[index]
	}

	target := selector.Target + selector.CostOfChange
	selection := make([]CoinSelectionEstimator, 0, len(estimators))
	amount := int64(0)
	for _, estimator := range estimators {
		selection = append(selection, estimator)
		amount += estimator.EffectiveAmount
		if amount >= target {
			return selection
		}
	}
	return []CoinSelectionEstimator{}
}

func coinSelectionAvailable(estimators []CoinSelectionEstimator) int64 {
	available := int64(0)
	for _, estimator := range estimators {
		available += estimator.EffectiveAmount
	}
	return available
}
