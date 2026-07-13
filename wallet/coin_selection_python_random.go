package wallet

import "unicode/utf8"

const (
	coinSelectionMTSize   = 624
	coinSelectionMTPeriod = 397
)

// coinSelectionPythonRandom is the _random.Random MT19937 core used by
// CPython. Coin selection only needs random(), so getrandbits and state APIs
// are deliberately outside this compatibility boundary.
type coinSelectionPythonRandom struct {
	state [coinSelectionMTSize]uint32
	index int
}

func newCoinSelectionPythonRandomFromInt(seed int64) *coinSelectionPythonRandom {
	var magnitude uint64
	if seed < 0 {
		magnitude = uint64(-(seed + 1)) + 1
	} else {
		magnitude = uint64(seed)
	}
	key := []uint32{uint32(magnitude)}
	if high := uint32(magnitude >> 32); high != 0 {
		key = append(key, high)
	}
	return newCoinSelectionPythonRandom(key)
}

func newCoinSelectionPythonRandomFromString(seed string) *coinSelectionPythonRandom {
	var hash uint64
	if seed != "" {
		first, _ := utf8.DecodeRuneInString(seed)
		hash = uint64(first) << 7
	}
	length := 0
	for _, character := range seed {
		hash = 1_000_003*hash ^ uint64(character)
		length++
	}
	hash ^= uint64(length)
	key := []uint32{uint32(hash)}
	if high := uint32(hash >> 32); high != 0 {
		key = append(key, high)
	}
	return newCoinSelectionPythonRandom(key)
}

func newCoinSelectionPythonRandom(key []uint32) *coinSelectionPythonRandom {
	random := &coinSelectionPythonRandom{}
	random.state[0] = 19_650_218
	for index := 1; index < coinSelectionMTSize; index++ {
		previous := random.state[index-1]
		random.state[index] = 1_812_433_253*(previous^(previous>>30)) + uint32(index)
	}

	stateIndex, keyIndex := 1, 0
	iterations := coinSelectionMTSize
	if len(key) > iterations {
		iterations = len(key)
	}
	for ; iterations > 0; iterations-- {
		previous := random.state[stateIndex-1]
		random.state[stateIndex] = (random.state[stateIndex] ^
			((previous ^ (previous >> 30)) * 1_664_525)) + key[keyIndex] + uint32(keyIndex)
		stateIndex++
		keyIndex++
		if stateIndex >= coinSelectionMTSize {
			random.state[0] = random.state[coinSelectionMTSize-1]
			stateIndex = 1
		}
		if keyIndex >= len(key) {
			keyIndex = 0
		}
	}
	for iterations = coinSelectionMTSize - 1; iterations > 0; iterations-- {
		previous := random.state[stateIndex-1]
		random.state[stateIndex] = (random.state[stateIndex] ^
			((previous ^ (previous >> 30)) * 1_566_083_941)) - uint32(stateIndex)
		stateIndex++
		if stateIndex >= coinSelectionMTSize {
			random.state[0] = random.state[coinSelectionMTSize-1]
			stateIndex = 1
		}
	}
	random.state[0] = 0x80000000
	random.index = coinSelectionMTSize
	return random
}

func (random *coinSelectionPythonRandom) Uint32() uint32 {
	if random.index >= coinSelectionMTSize {
		for index := 0; index < coinSelectionMTSize; index++ {
			next := index + 1
			if next == coinSelectionMTSize {
				next = 0
			}
			period := index + coinSelectionMTPeriod
			if period >= coinSelectionMTSize {
				period -= coinSelectionMTSize
			}
			value := (random.state[index] & 0x80000000) |
				(random.state[next] & 0x7fffffff)
			random.state[index] = random.state[period] ^ (value >> 1)
			if value&1 != 0 {
				random.state[index] ^= 0x9908b0df
			}
		}
		random.index = 0
	}
	value := random.state[random.index]
	random.index++
	value ^= value >> 11
	value ^= (value << 7) & 0x9d2c5680
	value ^= (value << 15) & 0xefc60000
	value ^= value >> 18
	return value
}

func (random *coinSelectionPythonRandom) Float64() float64 {
	high := uint64(random.Uint32() >> 5)
	low := uint64(random.Uint32() >> 6)
	return float64(high*67_108_864+low) / 9_007_199_254_740_992
}
