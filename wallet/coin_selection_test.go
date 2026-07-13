package wallet

import (
	"errors"
	"reflect"
	"testing"
)

const coinSelectionTestCent int64 = 1_000_000

func TestCoinSelectorEmptyInsufficientAndExactMatch(t *testing.T) {
	selector := NewCoinSelector(0, 0)
	selection, err := selector.Select(nil, "not-a-strategy")
	if err != nil || len(selection) != 0 {
		t.Fatalf("empty selection = %#v, %v", selection, err)
	}

	const fee int64 = 7_400
	pool := make([]CoinSelectionEstimator, 100)
	for index := range pool {
		pool[index] = coinSelectionTestEstimator(coinSelectionTestCent+fee, fee, 1, uint32(index))
	}
	selector = NewCoinSelector(101*coinSelectionTestCent, 0)
	selection, err = selector.Select(pool, CoinSelectionStrategyStandard)
	if err != nil || len(selection) != 0 || selector.Tries != 0 {
		t.Fatalf("insufficient selection = %d coins, tries %d, %v", len(selection), selector.Tries, err)
	}

	selector = NewCoinSelector(100*coinSelectionTestCent, 0)
	selection, err = selector.Select(pool, CoinSelectionStrategyStandard)
	if err != nil || len(selection) != 100 || selector.Tries != 201 || !selector.ExactMatch {
		t.Fatalf("complete selection = %d coins, tries %d, exact %v, %v",
			len(selection), selector.Tries, selector.ExactMatch, err)
	}

	pool = []CoinSelectionEstimator{
		coinSelectionTestEstimator(coinSelectionTestCent+fee, fee, 1, 1),
		coinSelectionTestEstimator(coinSelectionTestCent, fee, 1, 2),
		coinSelectionTestEstimator(coinSelectionTestCent-fee, fee, 1, 3),
	}
	selector = NewCoinSelector(coinSelectionTestCent, 0)
	selection, err = selector.Select(pool, CoinSelectionStrategyStandard)
	if err != nil || !selector.ExactMatch ||
		!reflect.DeepEqual(coinSelectionTestAmounts(selection), []int64{coinSelectionTestCent + fee}) {
		t.Fatalf("exact selection = %v, exact %v, %v",
			coinSelectionTestAmounts(selection), selector.ExactMatch, err)
	}
}

func TestCoinSelectorStandardAndConfirmedStrategies(t *testing.T) {
	const fee int64 = 7_400
	pool := []CoinSelectionEstimator{
		coinSelectionTestEstimator(1*coinSelectionTestCent, fee, 1, 1),
		coinSelectionTestEstimator(1*coinSelectionTestCent, fee, 1, 2),
		coinSelectionTestEstimator(3*coinSelectionTestCent, fee, 1, 3),
		coinSelectionTestEstimator(5*coinSelectionTestCent, fee, 1, 4),
		coinSelectionTestEstimator(10*coinSelectionTestCent, fee, 1, 5),
	}
	selector := NewCoinSelector(3*coinSelectionTestCent, 0)
	selection, err := selector.Select(pool, CoinSelectionStrategyStandard)
	if err != nil || selector.ExactMatch ||
		!reflect.DeepEqual(coinSelectionTestAmounts(selection), []int64{5 * coinSelectionTestCent}) {
		t.Fatalf("standard selection = %v, exact %v, %v",
			coinSelectionTestAmounts(selection), selector.ExactMatch, err)
	}

	pool = []CoinSelectionEstimator{
		coinSelectionTestEstimator(2*coinSelectionTestCent, fee, 1, 1),
		coinSelectionTestEstimator(3*coinSelectionTestCent, fee, 1, 2),
		coinSelectionTestEstimator(4*coinSelectionTestCent, fee, 1, 3),
	}
	selector = NewCoinSelectorWithSeed(coinSelectionTestCent, 0, 0)
	selection, err = selector.Select(pool, CoinSelectionStrategyStandard)
	if err != nil || selector.ExactMatch ||
		!reflect.DeepEqual(coinSelectionTestAmounts(selection), []int64{2 * coinSelectionTestCent}) {
		t.Fatalf("closest selection = %v, exact %v, %v",
			coinSelectionTestAmounts(selection), selector.ExactMatch, err)
	}

	confirmedPool := []CoinSelectionEstimator{
		coinSelectionTestEstimator(11*coinSelectionTestCent, fee, 5, 1),
		coinSelectionTestEstimator(11*coinSelectionTestCent, fee, 0, 2),
		coinSelectionTestEstimator(11*coinSelectionTestCent, fee, -2, 3),
		coinSelectionTestEstimator(11*coinSelectionTestCent, fee, 5, 4),
	}
	selector = NewCoinSelectorWithSeed(20*coinSelectionTestCent, 0, 0)
	selection, err = selector.Select(confirmedPool, CoinSelectionStrategyOnlyConfirmed)
	if err != nil || !reflect.DeepEqual(coinSelectionTestHeights(selection), []int64{5, 5}) {
		t.Fatalf("only-confirmed selection = %v, %v", coinSelectionTestHeights(selection), err)
	}

	selector = NewCoinSelectorWithSeed(25*coinSelectionTestCent, 0, 0)
	selection, err = selector.Select(confirmedPool, CoinSelectionStrategyOnlyConfirmed)
	if err != nil || len(selection) != 0 {
		t.Fatalf("insufficient confirmed selection = %v, %v", coinSelectionTestHeights(selection), err)
	}

	selector = NewCoinSelectorWithSeed(20*coinSelectionTestCent, 0, 0)
	selection, err = selector.Select(confirmedPool, CoinSelectionStrategyPreferConfirmed)
	if err != nil || !reflect.DeepEqual(coinSelectionTestHeights(selection), []int64{5, 5}) {
		t.Fatalf("prefer-confirmed selection = %v, %v", coinSelectionTestHeights(selection), err)
	}

	selector = NewCoinSelectorWithStringSeed(25*coinSelectionTestCent, 0, "\x00")
	selection, err = selector.Select(confirmedPool, CoinSelectionStrategyPreferConfirmed)
	if err != nil || !reflect.DeepEqual(coinSelectionTestHeights(selection), []int64{5, 0, -2}) {
		t.Fatalf("prefer-confirmed fallback = heights %v total %d, %v",
			coinSelectionTestHeights(selection), coinSelectionTestEffectiveTotal(selection), err)
	}
}

func TestCoinSelectorBranchAndBoundPinnedVectors(t *testing.T) {
	pool := coinSelectionTestPool(1, 2, 3, 4)
	for _, test := range []struct {
		name         string
		target       int64
		costOfChange int64
		want         []int64
	}{
		{"one cent", 1 * coinSelectionTestCent, coinSelectionTestCent / 2, []int64{1 * coinSelectionTestCent}},
		{"two cents", 2 * coinSelectionTestCent, coinSelectionTestCent / 2, []int64{2 * coinSelectionTestCent}},
		{"five cents", 5 * coinSelectionTestCent, coinSelectionTestCent / 2, []int64{3 * coinSelectionTestCent, 2 * coinSelectionTestCent}},
		{"unavailable", 11 * coinSelectionTestCent, coinSelectionTestCent / 2, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			selector := NewCoinSelector(test.target, test.costOfChange)
			selection, err := selector.Select(pool, CoinSelectionStrategyBranchAndBound)
			if err != nil || !reflect.DeepEqual(coinSelectionTestAmounts(selection), test.want) {
				t.Fatalf("selection = %v, want %v, %v", coinSelectionTestAmounts(selection), test.want, err)
			}
		})
	}

	pool = append(pool, coinSelectionTestEstimator(5*coinSelectionTestCent, 0, 1, 5))
	selector := NewCoinSelector(10*coinSelectionTestCent, coinSelectionTestCent/2)
	selection, err := selector.Select(pool, CoinSelectionStrategyBranchAndBound)
	if err != nil || !reflect.DeepEqual(
		coinSelectionTestAmounts(selection),
		[]int64{4 * coinSelectionTestCent, 3 * coinSelectionTestCent, 2 * coinSelectionTestCent, 1 * coinSelectionTestCent},
	) {
		t.Fatalf("ten-cent selection = %v, %v", coinSelectionTestAmounts(selection), err)
	}

	selector = NewCoinSelector(10*coinSelectionTestCent, 5_000)
	selection, err = selector.Select(pool, CoinSelectionStrategyBranchAndBound)
	if err != nil || !reflect.DeepEqual(
		coinSelectionTestAmounts(selection),
		[]int64{4 * coinSelectionTestCent, 3 * coinSelectionTestCent, 2 * coinSelectionTestCent, 1 * coinSelectionTestCent},
	) {
		t.Fatalf("ten-cent waste selection = %v, %v", coinSelectionTestAmounts(selection), err)
	}

	selector = NewCoinSelector(coinSelectionTestCent/4, coinSelectionTestCent/2)
	selection, err = selector.Select(pool, CoinSelectionStrategyBranchAndBound)
	if err != nil || len(selection) != 0 {
		t.Fatalf("too-large pool selection = %v, %v", coinSelectionTestAmounts(selection), err)
	}
}

func TestCoinSelectorBranchAndBoundExhaustionAndDuplicateSkip(t *testing.T) {
	pool, target := coinSelectionTestHardCase(17)
	selector := NewCoinSelector(target, 0)
	selection, err := selector.Select(pool, CoinSelectionStrategyBranchAndBound)
	if err != nil || len(selection) != 0 || selector.Tries != CoinSelectionMaximumTries {
		t.Fatalf("hard selection = %d coins, tries %d, %v", len(selection), selector.Tries, err)
	}

	pool = make([]CoinSelectionEstimator, 0, 50_005)
	for index := uint32(0); index < 4; index++ {
		pool = append(pool, coinSelectionTestEstimator(7*coinSelectionTestCent, 0, 1, index))
	}
	pool = append(pool, coinSelectionTestEstimator(2*coinSelectionTestCent, 0, 1, 4))
	for index := uint32(0); index < 50_000; index++ {
		pool = append(pool, coinSelectionTestEstimator(5*coinSelectionTestCent, 0, 1, index+5))
	}
	selector = NewCoinSelector(30*coinSelectionTestCent, 5_000)
	selection, err = selector.Select(pool, CoinSelectionStrategyBranchAndBound)
	if err != nil || !reflect.DeepEqual(
		coinSelectionTestAmounts(selection),
		[]int64{7 * coinSelectionTestCent, 7 * coinSelectionTestCent, 7 * coinSelectionTestCent,
			7 * coinSelectionTestCent, 2 * coinSelectionTestCent},
	) {
		t.Fatalf("duplicate selection = %v, tries %d, %v",
			coinSelectionTestAmounts(selection), selector.Tries, err)
	}

	differentFees := []CoinSelectionEstimator{
		coinSelectionTestEstimatorWithEffective(6, 6, 1, 1, 1),
		coinSelectionTestEstimatorWithEffective(7, 6, 2, 1, 2),
		coinSelectionTestEstimatorWithEffective(4, 4, 0, 1, 3),
	}
	selector = NewCoinSelector(10, 0)
	selection, err = selector.Select(differentFees, CoinSelectionStrategyBranchAndBound)
	if err != nil || !reflect.DeepEqual(coinSelectionTestInputIDs(selection), []uint32{2, 3}) {
		t.Fatalf("different-fee duplicate selection = ids %v, %v", coinSelectionTestInputIDs(selection), err)
	}
}

func TestCoinSelectorRandomDrawAndInPlaceMutation(t *testing.T) {
	pool := coinSelectionTestPool(1, 2, 3, 4)
	selector := NewCoinSelectorWithSeed(5*coinSelectionTestCent, coinSelectionTestCent, 7)
	selection, err := selector.Select(pool, CoinSelectionStrategyRandomDraw)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection) == 0 || !reflect.DeepEqual(selection, pool[:len(selection)]) {
		t.Fatalf("random selection is not a prefix of shuffled pool: %#v / %#v", selection, pool)
	}
	target := selector.Target + selector.CostOfChange
	if total := coinSelectionTestEffectiveTotal(selection); total < target {
		t.Fatalf("random total = %d, want at least %d", total, target)
	}
	if len(selection) > 1 && coinSelectionTestEffectiveTotal(selection[:len(selection)-1]) >= target {
		t.Fatalf("random draw selected more inputs than its shuffled prefix required")
	}

	selector = NewCoinSelectorWithSeed(20*coinSelectionTestCent, 0, 7)
	selection, err = selector.Select(pool, CoinSelectionStrategyRandomDraw)
	if err != nil || len(selection) != 0 {
		t.Fatalf("insufficient random draw = %v, %v", coinSelectionTestAmounts(selection), err)
	}

	unsorted := coinSelectionTestPool(1, 4, 2, 3)
	selector = NewCoinSelector(100*coinSelectionTestCent, 0)
	_ = selector.BranchAndBound(unsorted, coinSelectionTestEffectiveTotal(unsorted))
	if !reflect.DeepEqual(
		coinSelectionTestAmounts(unsorted),
		[]int64{4 * coinSelectionTestCent, 3 * coinSelectionTestCent, 2 * coinSelectionTestCent, 1 * coinSelectionTestCent},
	) {
		t.Fatalf("branch-and-bound did not sort in place: %v", coinSelectionTestAmounts(unsorted))
	}
}

func TestCoinSelectorPythonVersionOneStringSeedAndLegacyShuffle(t *testing.T) {
	random := newCoinSelectionPythonRandomFromString("\x00")
	wantRandom := []float64{
		0.13436424411240122,
		0.8474337369372327,
		0.763774618976614,
		0.2550690257394217,
		0.49543508709194095,
	}
	for index, want := range wantRandom {
		if got := random.Float64(); got != want {
			t.Fatalf("Python random value %d = %.17g, want %.17g", index, got, want)
		}
	}
	for seed, want := range map[string]float64{
		"":    0.8444218515250481,
		"abc": 0.09639374978773074,
	} {
		if got := newCoinSelectionPythonRandomFromString(seed).Float64(); got != want {
			t.Fatalf("Python random seed %q = %.17g, want %.17g", seed, got, want)
		}
	}

	pool := coinSelectionTestPool(2, 3, 4)
	selector := NewCoinSelectorWithStringSeed(coinSelectionTestCent, 0, "\x00")
	selection, err := selector.Select(pool, CoinSelectionStrategyRandomDraw)
	if err != nil || !reflect.DeepEqual(
		coinSelectionTestAmounts(pool),
		[]int64{4 * coinSelectionTestCent, 3 * coinSelectionTestCent, 2 * coinSelectionTestCent},
	) || !reflect.DeepEqual(
		coinSelectionTestAmounts(selection), []int64{4 * coinSelectionTestCent},
	) {
		t.Fatalf("Python legacy shuffle = pool %v selection %v, %v",
			coinSelectionTestAmounts(pool), coinSelectionTestAmounts(selection), err)
	}
}

func TestCoinSelectorStrategyResolutionOrdering(t *testing.T) {
	selector := NewCoinSelector(1, 0)
	selection, err := selector.Select(nil, "invalid")
	if err != nil || len(selection) != 0 {
		t.Fatalf("empty invalid strategy = %#v, %v", selection, err)
	}

	selection, err = selector.Select(
		[]CoinSelectionEstimator{coinSelectionTestEstimator(1, 0, 1, 1)}, "invalid",
	)
	if selection != nil || !errors.Is(err, ErrUnknownCoinSelectionStrategy) {
		t.Fatalf("invalid strategy = %#v, %v", selection, err)
	}
}

func coinSelectionTestEstimator(
	amount, fee, height int64, inputID uint32,
) CoinSelectionEstimator {
	return coinSelectionTestEstimatorWithEffective(amount, amount-fee, fee, height, inputID)
}

func coinSelectionTestEstimatorWithEffective(
	amount, effectiveAmount, fee, height int64, inputID uint32,
) CoinSelectionEstimator {
	output := &TransactionOutput{Amount: uint64(amount)}
	return CoinSelectionEstimator{
		TransactionSpendable: TransactionSpendable{
			Input: TransactionInput{
				PreviousIndex:  inputID,
				ResolvedOutput: output,
			},
			EffectiveAmount: effectiveAmount,
		},
		Fee:    fee,
		Height: height,
	}
}

func coinSelectionTestPool(coins ...int64) []CoinSelectionEstimator {
	pool := make([]CoinSelectionEstimator, len(coins))
	for index, coins := range coins {
		pool[index] = coinSelectionTestEstimator(
			coins*coinSelectionTestCent, 0, 1, uint32(index),
		)
	}
	return pool
}

func coinSelectionTestHardCase(count int) ([]CoinSelectionEstimator, int64) {
	pool := make([]CoinSelectionEstimator, 0, count*2)
	target := int64(0)
	for index := 0; index < count; index++ {
		amount := int64(1) << (count + index)
		target += amount
		pool = append(pool,
			coinSelectionTestEstimator(amount, 0, 1, uint32(index*2)),
			coinSelectionTestEstimator(
				amount+(int64(1)<<(count-1-index)), 0, 1, uint32(index*2+1),
			),
		)
	}
	return pool, target
}

func coinSelectionTestAmounts(selection []CoinSelectionEstimator) []int64 {
	if len(selection) == 0 {
		return nil
	}
	amounts := make([]int64, len(selection))
	for index, estimator := range selection {
		amounts[index] = int64(estimator.Input.ResolvedOutput.Amount)
	}
	return amounts
}

func coinSelectionTestHeights(selection []CoinSelectionEstimator) []int64 {
	heights := make([]int64, len(selection))
	for index, estimator := range selection {
		heights[index] = estimator.Height
	}
	return heights
}

func coinSelectionTestInputIDs(selection []CoinSelectionEstimator) []uint32 {
	ids := make([]uint32, len(selection))
	for index, estimator := range selection {
		ids[index] = estimator.Input.PreviousIndex
	}
	return ids
}

func coinSelectionTestEffectiveTotal(selection []CoinSelectionEstimator) int64 {
	return coinSelectionAvailable(selection)
}
