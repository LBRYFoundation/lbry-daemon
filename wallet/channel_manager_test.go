package wallet

import (
	"encoding/hex"
	"errors"
	"reflect"
	"sync"
	"testing"

	"lbry/daemon/wallet/keys"
)

func TestDeterministicChannelKeyGoldenDerivation(t *testing.T) {
	account := channelManagerSeedAccount(t)
	manager := account.DeterministicChannelKeys
	root, err := manager.PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	assertChannelKey(t, root,
		"9b3241fdb9489d2a67ac41d0c810ed2a99be5c6a3d2d53fd920b34c75adec607",
		"030c0ec6e1bc1a1e3c34bdcf239d4dd96b876742fea492d3ea202c20e341b54a75",
		"bXom2imrrUpFcUCAq1ZEd91SWuD7Y7yra5",
	)
	for index, want := range []struct {
		private string
		public  string
		address string
	}{
		{"d15236863cd6d24457291b0906faf01fc92e522c21a34b051191210411e8f33f", "02ab4d06c662bc0967b05d36a56de31dbd3ab3fc9cf8c399d4b640c3a4b9a6e68d", "bQQXxPRBW9TGp4VSA9vqoBfgCN38eMBcA7"},
		{"256de4d6f3afbfee31746e9ee3cd9395e3e995f507ba97c4e048906d6a2569ed", "034fff33e686a258c3e1ce5bffa021a4b49fbf94d728c0eb48c029d620f119fc28", "bFFn3mJRnEPvJs6KZAGy7C8RLBABELGMbF"},
		{"9e6d09892ac1b754395e7dcf92916dd688d2421ac1c2eb4c676e0495a0219ebd", "020d07b813e642b26b8e5117ad51505cc28d091ca49664b907dd2b95decd73e6ef", "bHH2Nf9NUM6uoTHkNi1sTrBxkQ559YAkD1"},
	} {
		child, err := root.Child(int64(index))
		if err != nil {
			t.Fatal(err)
		}
		assertChannelKey(t, child, want.private, want.public, want.address)
	}
}

func TestDeterministicChannelGenerateCachesBeforeProbeAndIsIdempotent(t *testing.T) {
	account := channelManagerSeedAccount(t)
	manager := account.DeterministicChannelKeys
	responses := []bool{true, true, false}
	var probed []string
	probe := func(_ *Account, publicKey *keys.PublicKey) (bool, error) {
		probed = append(probed, publicKey.Address())
		response := responses[0]
		responses = responses[1:]
		return response, nil
	}
	key, err := manager.GenerateNextKey(probe)
	if err != nil {
		t.Fatal(err)
	}
	if key.Address() != "bHH2Nf9NUM6uoTHkNi1sTrBxkQ559YAkD1" || manager.LastKnown != 2 {
		t.Fatalf("generated key = %s last_known %d", key.Address(), manager.LastKnown)
	}
	if want := []string{
		"bQQXxPRBW9TGp4VSA9vqoBfgCN38eMBcA7",
		"bFFn3mJRnEPvJs6KZAGy7C8RLBABELGMbF",
		"bHH2Nf9NUM6uoTHkNi1sTrBxkQ559YAkD1",
	}; !reflect.DeepEqual(probed, want) {
		t.Fatalf("probe order = %v, want %v", probed, want)
	}
	if len(manager.Cache) != 3 || manager.GetPrivateKey(key.Address()) != key {
		t.Fatalf("cache = %v", manager.Cache)
	}

	keyAgain, err := manager.GenerateNextKey(func(_ *Account, publicKey *keys.PublicKey) (bool, error) {
		return false, nil
	})
	if err != nil || keyAgain.Address() != key.Address() || manager.LastKnown != 2 {
		t.Fatalf("idempotent candidate = %v last_known %d, %v", keyAgain, manager.LastKnown, err)
	}

	errorManager := NewDeterministicChannelKeyManager(account)
	probeError := errors.New("usage probe failed")
	_, err = errorManager.GenerateNextKey(func(_ *Account, publicKey *keys.PublicKey) (bool, error) {
		return false, probeError
	})
	if !errors.Is(err, probeError) || errorManager.LastKnown != 0 || len(errorManager.Cache) != 1 {
		t.Fatalf("probe error state = last_known %d cache %d error %v",
			errorManager.LastKnown, len(errorManager.Cache), err)
	}
}

func TestDeterministicChannelObservationPrimingAndLockCache(t *testing.T) {
	account := channelManagerSeedAccount(t)
	manager := account.DeterministicChannelKeys
	root, err := manager.PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	first, _ := root.Child(0)
	second, _ := root.Child(1)
	matched, err := manager.MaybeGenerateForChannel(second.PublicKey().CompressedBytes())
	if err != nil || matched || manager.LastKnown != 0 || len(manager.Cache) != 0 {
		t.Fatalf("scan-ahead observation = matched %v last %d cache %d, %v",
			matched, manager.LastKnown, len(manager.Cache), err)
	}
	matched, err = manager.MaybeGenerateForChannel(first.PublicKey().CompressedBytes())
	if err != nil || !matched || manager.LastKnown != 1 || manager.GetPrivateKey(first.Address()) == nil {
		t.Fatalf("current observation = matched %v last %d, %v", matched, manager.LastKnown, err)
	}

	if err := account.Encrypt("password"); err != nil {
		t.Fatal(err)
	}
	if account.PrivateKey != nil {
		t.Fatal("account encryption retained account private key")
	}
	cachedRoot, err := manager.PrivateKey()
	if err != nil || cachedRoot != root {
		t.Fatalf("locked cached root = %p, want %p, %v", cachedRoot, root, err)
	}

	readOnly, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "public_key", Value: fixedAccountXPub}, Member{Key: "modified_on", Value: 1},
	))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := readOnly.DeterministicChannelKeys.EnsureCachePrimed(func(*Account, *keys.PublicKey) (bool, error) {
		called = true
		return false, nil
	}); err != nil || called {
		t.Fatalf("read-only priming = called %v, %v", called, err)
	}
	if _, err := readOnly.DeterministicChannelKeys.GenerateNextKey(func(*Account, *keys.PublicKey) (bool, error) {
		return false, nil
	}); !errors.Is(err, ErrAccountPrivateKeyMissing) {
		t.Fatalf("read-only generation error = %v", err)
	}
}

func TestDeterministicChannelConcurrentObservationAdvancesOnce(t *testing.T) {
	account := channelManagerSeedAccount(t)
	manager := account.DeterministicChannelKeys
	root, err := manager.PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := root.Child(0)
	if err != nil {
		t.Fatal(err)
	}

	const observations = 64
	start := make(chan struct{})
	results := make(chan bool, observations)
	errors := make(chan error, observations)
	var wait sync.WaitGroup
	wait.Add(observations)
	for index := 0; index < observations; index++ {
		go func() {
			defer wait.Done()
			<-start
			matched, err := manager.MaybeGenerateForChannel(candidate.PublicKey().CompressedBytes())
			results <- matched
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)

	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	matches := 0
	for matched := range results {
		if matched {
			matches++
		}
	}
	if matches != 1 || manager.LastKnown != 1 ||
		manager.GetPrivateKey(candidate.Address()) == nil || len(manager.Cache) != 1 {
		t.Fatalf(
			"concurrent state = matches %d last_known %d cache %d",
			matches, manager.LastKnown, len(manager.Cache),
		)
	}
}

func TestAccountGetChannelPrivateKeyUsesDeterministicCacheFallback(t *testing.T) {
	account := channelManagerSeedAccount(t)
	key, err := account.DeterministicChannelKeys.GenerateNextKey(
		func(*Account, *keys.PublicKey) (bool, error) { return false, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := account.GetChannelPrivateKey(key.PublicKey().CompressedBytes(), nil)
	if err != nil || loaded != key {
		t.Fatalf("deterministic cached lookup = %p, want %p, %v", loaded, key, err)
	}
}

func channelManagerSeedAccount(t *testing.T) *Account {
	t.Helper()
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 1},
	))
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func assertChannelKey(t *testing.T, key *keys.PrivateKey, private, public, address string) {
	t.Helper()
	if got := hex.EncodeToString(key.PrivateKeyBytes()); got != private {
		t.Fatalf("private key = %s, want %s", got, private)
	}
	if got := hex.EncodeToString(key.PublicKey().CompressedBytes()); got != public {
		t.Fatalf("public key = %s, want %s", got, public)
	}
	if got := key.Address(); got != address {
		t.Fatalf("address = %s, want %s", got, address)
	}
}
