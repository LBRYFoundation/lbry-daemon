package wallet

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
)

const (
	addressSyncOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	addressSyncOraclePinnedVersion = "0.113.0"
	addressSyncOracleXPub          = "xpub661MyMwAqRbcGWtPvbWh9sc2BCfw2cTeVDYF23o3N1t6UZ5wv3EMmDgp66FxH" +
		"uDtWdft3B5eL5xQtyzAtkdmhhC95gjRjLzSTdkho95asu9"
)

var addressSyncOraclePinnedSources = map[string]string{
	"lbry/__init__.py":                          "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/crypto/hash.py":                       "bfc430bd3fe98578b406caa3a8e2116a40f492c7b68e269176e838b4ef426a72",
	"lbry/wallet/account.py":                    "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
	"lbry/wallet/database.py":                   "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
	"lbry/wallet/ledger.py":                     "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
	"lbry/wallet/network.py":                    "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
	"tests/integration/blockchain/test_sync.py": "2788fc83e701eb0082fa4167cbada29e3eccb0e008f77df8e2c5a2810354950d",
	"tests/unit/wallet/test_account.py":         "3f6ae1c40230ce2717c44157f757cb96e24f31b7402542d4ad987905826e1c62",
	"tests/unit/wallet/test_database.py":        "7af85de707b329d8715cd22419a4f761b10792a3ecc023202389dd86e3011c51",
	"tests/unit/wallet/test_ledger.py":          "045a14bc252c0b9b6759d7444e582c5e17c6009689f5ee1fd05e74739711ab88",
}

type addressSyncOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion                   string   `json:"python_version"`
		PythonAssertions                bool     `json:"python_assertions"`
		ReceivingChain                  int      `json:"receiving_chain"`
		ChangeChain                     int      `json:"change_chain"`
		SubscribeBatchSize              int      `json:"subscribe_batch_size"`
		SubscribeRPC                    string   `json:"subscribe_rpc"`
		HistoryRPC                      string   `json:"history_rpc"`
		NotificationMethod              string   `json:"notification_method"`
		HistoryFormat                   string   `json:"history_format"`
		StatusAlgorithm                 string   `json:"status_algorithm"`
		StatusEmptyIsNull               bool     `json:"status_empty_is_null"`
		ExistingSubscriptionPrecedesGap bool     `json:"existing_subscription_precedes_gap"`
		AccountManagerOrder             []string `json:"account_manager_order"`
	} `json:"metadata"`
	Addresses struct {
		Defaults                   addressSyncOracleDefaults  `json:"defaults"`
		GapSteps                   []addressSyncOracleGapStep `json:"gap_steps"`
		Inventory                  addressSyncOracleInventory `json:"inventory"`
		AllUnusedMaxGap            int                        `json:"all_unused_max_gap"`
		LockGuard                  addressSyncOracleError     `json:"lock_guard"`
		PartialAnnouncementFailure struct {
			ErrorType     *string                         `json:"error_type"`
			ErrorMessage  *string                         `json:"error_message"`
			Persisted     []addressSyncOracleRecord       `json:"persisted"`
			Announcements []addressSyncOracleAnnouncement `json:"announcements"`
			RetryCreated  []string                        `json:"retry_created"`
		} `json:"partial_announcement_failure"`
		SingleAddress struct {
			SameManager          bool                            `json:"same_manager"`
			FirstCreated         []string                        `json:"first_created"`
			SecondCreated        []string                        `json:"second_created"`
			UsableAfterThreeUses string                          `json:"usable_after_three_uses"`
			Records              []addressSyncOracleRecord       `json:"records"`
			Announcements        []addressSyncOracleAnnouncement `json:"announcements"`
		} `json:"single_address"`
	} `json:"addresses"`
	StatusCases []addressSyncOracleStatusCase `json:"status_cases"`
}

type addressSyncOracleRecord struct {
	Address   string  `json:"address"`
	Chain     int     `json:"chain"`
	N         int     `json:"n"`
	History   *string `json:"history"`
	UsedTimes int     `json:"used_times"`
}

type addressSyncOracleAnnouncement struct {
	Chain     int      `json:"chain"`
	Addresses []string `json:"addresses"`
}

type addressSyncOracleDefaults struct {
	Generator            string                          `json:"generator"`
	ReceivingGap         int                             `json:"receiving_gap"`
	ChangeGap            int                             `json:"change_gap"`
	ReceivingMaximumUses int                             `json:"receiving_maximum_uses"`
	ChangeMaximumUses    int                             `json:"change_maximum_uses"`
	Created              []string                        `json:"created"`
	Announcements        []addressSyncOracleAnnouncement `json:"announcements"`
	ReceivingRecords     []addressSyncOracleRecord       `json:"receiving_records"`
	ChangeRecords        []addressSyncOracleRecord       `json:"change_records"`
}

type addressSyncOracleGapStep struct {
	Name          string                          `json:"name"`
	Gap           int                             `json:"gap"`
	Before        []addressSyncOracleRecord       `json:"before"`
	Created       []string                        `json:"created"`
	After         []addressSyncOracleRecord       `json:"after"`
	Announcements []addressSyncOracleAnnouncement `json:"announcements"`
}

type addressSyncOracleInventory struct {
	MaximumUses int                       `json:"maximum_uses"`
	All         []addressSyncOracleRecord `json:"all"`
	Usable      []addressSyncOracleRecord `json:"usable"`
	MaxGap      int                       `json:"max_gap"`
}

type addressSyncOracleError struct {
	ErrorType    *string `json:"error_type"`
	ErrorMessage *string `json:"error_message"`
}

type addressSyncOracleStatusResult struct {
	Status  *string `json:"status"`
	History [][]any `json:"history"`
}

type addressSyncOracleStatusCase struct {
	Name             string                         `json:"name"`
	Address          string                         `json:"address"`
	Supplied         bool                           `json:"supplied"`
	InputHistory     *string                        `json:"input_history"`
	EffectiveHistory string                         `json:"effective_history"`
	Result           *addressSyncOracleStatusResult `json:"result"`
	ErrorType        *string                        `json:"error_type"`
	ErrorMessage     *string                        `json:"error_message"`
}

func TestAddressSyncHelpersMatchPinnedPythonOracle(t *testing.T) {
	oracle := runAddressSyncOracle(t)
	if oracle.Reference.Commit != addressSyncOraclePinnedCommit ||
		oracle.Reference.Version != addressSyncOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, addressSyncOraclePinnedSources) {
		t.Fatalf("address sync oracle reference = %+v", oracle.Reference)
	}
	metadata := oracle.Metadata
	if !metadata.PythonAssertions || metadata.ReceivingChain != ReceiveChain ||
		metadata.ChangeChain != ChangeChain || metadata.SubscribeBatchSize != SPVAddressSubscriptionBatchSize ||
		metadata.SubscribeRPC != SPVAddressSubscribeMethod ||
		metadata.HistoryRPC != SPVAddressHistoryMethod ||
		metadata.NotificationMethod != metadata.SubscribeRPC ||
		metadata.HistoryFormat != "txid:height:" || metadata.StatusAlgorithm != "sha256" ||
		!metadata.StatusEmptyIsNull || !metadata.ExistingSubscriptionPrecedesGap ||
		!reflect.DeepEqual(metadata.AccountManagerOrder, []string{"receiving", "change"}) {
		t.Fatalf("address sync oracle metadata = %+v", metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && metadata.PythonVersion != want {
		t.Fatalf("address sync oracle Python version = %q, want %q", metadata.PythonVersion, want)
	}

	assertAddressSyncOracleDefaults(t, oracle.Addresses.Defaults)
	for _, step := range oracle.Addresses.GapSteps {
		step := step
		t.Run("gap/"+step.Name, func(t *testing.T) {
			start, count := addressSyncOracleGapPlan(step.Before, step.Gap)
			if count != len(step.Created) {
				t.Fatalf("gap plan = start %d count %d, Python created %v", start, count, step.Created)
			}
			wantCreated := make([]string, count)
			for index := range wantCreated {
				wantCreated[index] = "address-0-" + strconv.Itoa(start+index)
			}
			if !reflect.DeepEqual(step.Created, wantCreated) {
				t.Fatalf("created = %v, want %v", step.Created, wantCreated)
			}
			modeled := append([]addressSyncOracleRecord(nil), step.Before...)
			for index, address := range step.Created {
				modeled = append(modeled, addressSyncOracleRecord{Address: address, Chain: 0, N: start + index})
			}
			modeled = addressSyncOracleOrder(modeled, nil)
			if !reflect.DeepEqual(modeled, step.After) {
				t.Fatalf("modeled records = %#v, Python %#v", modeled, step.After)
			}
			if count == 0 && len(step.Announcements) != 0 {
				t.Fatalf("full gap announced %v", step.Announcements)
			}
			if count > 0 && !reflect.DeepEqual(step.Announcements, []addressSyncOracleAnnouncement{{
				Chain: 0, Addresses: step.Created,
			}}) {
				t.Fatalf("announcements = %#v", step.Announcements)
			}
		})
	}

	all := addressSyncOracleOrder(oracle.Addresses.Inventory.All, nil)
	if !reflect.DeepEqual(all, oracle.Addresses.Inventory.All) {
		t.Fatalf("inventory order = %#v, Python %#v", all, oracle.Addresses.Inventory.All)
	}
	maximumUses := oracle.Addresses.Inventory.MaximumUses
	usable := addressSyncOracleOrder(oracle.Addresses.Inventory.All, &maximumUses)
	if !reflect.DeepEqual(usable, oracle.Addresses.Inventory.Usable) {
		t.Fatalf("usable inventory = %#v, Python %#v", usable, oracle.Addresses.Inventory.Usable)
	}
	if got := addressSyncOracleMaxGap(oracle.Addresses.Inventory.All); got != oracle.Addresses.Inventory.MaxGap {
		t.Fatalf("max gap = %d, Python %d", got, oracle.Addresses.Inventory.MaxGap)
	}
	allUnused := []addressSyncOracleRecord{{N: 0}, {N: 1}, {N: 2}, {N: 3}}
	if got := addressSyncOracleMaxGap(allUnused); got != oracle.Addresses.AllUnusedMaxGap || got != 0 {
		t.Fatalf("all-unused max gap = %d, Python %d", got, oracle.Addresses.AllUnusedMaxGap)
	}

	assertAddressSyncOracleFailures(t, oracle)
	for _, fixture := range oracle.StatusCases {
		fixture := fixture
		t.Run("status/"+fixture.Name, func(t *testing.T) {
			status, history, err := LocalAddressStatusAndHistory(fixture.EffectiveHistory)
			if fixture.ErrorType != nil {
				if err == nil || *fixture.ErrorType != "ValueError" || fixture.ErrorMessage == nil ||
					!strings.Contains(*fixture.ErrorMessage, "not-int") {
					t.Fatalf("status error = %v, Python %v/%v", err, fixture.ErrorType, fixture.ErrorMessage)
				}
				return
			}
			result := addressSyncOracleStatusResult{
				Status: status, History: make([][]any, len(history)),
			}
			for index, entry := range history {
				result.History[index] = []any{entry.TxHash, json.Number(strconv.FormatInt(entry.Height, 10))}
			}
			if err != nil || fixture.Result == nil || !reflect.DeepEqual(result, *fixture.Result) {
				t.Fatalf("status = %+v, %v; Python %+v", result, err, fixture.Result)
			}
		})
	}
}

func assertAddressSyncOracleDefaults(t *testing.T, fixture addressSyncOracleDefaults) {
	t.Helper()
	if fixture.Generator != DeterministicChainGenerator || fixture.ReceivingGap != 20 ||
		fixture.ChangeGap != 6 || fixture.ReceivingMaximumUses != 1 || fixture.ChangeMaximumUses != 1 ||
		len(fixture.Created) != 26 || len(fixture.ReceivingRecords) != 20 ||
		len(fixture.ChangeRecords) != 6 || len(fixture.Announcements) != 2 {
		t.Fatalf("default address fixture = %+v", fixture)
	}
	for index, record := range append(fixture.ReceivingRecords, fixture.ChangeRecords...) {
		chain := 0
		child := index
		if index >= 20 {
			chain, child = 1, index-20
		}
		want := addressSyncOracleRecord{
			Address: "address-" + strconv.Itoa(chain) + "-" + strconv.Itoa(child),
			Chain:   chain, N: child,
		}
		if !reflect.DeepEqual(record, want) || fixture.Created[index] != want.Address {
			t.Fatalf("default record %d = %+v/%q, want %+v", index, record, fixture.Created[index], want)
		}
	}
	if fixture.Announcements[0].Chain != ReceiveChain || fixture.Announcements[1].Chain != ChangeChain ||
		!reflect.DeepEqual(fixture.Announcements[0].Addresses, fixture.Created[:20]) ||
		!reflect.DeepEqual(fixture.Announcements[1].Addresses, fixture.Created[20:]) {
		t.Fatalf("default announcements = %#v", fixture.Announcements)
	}

	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "public_key", Value: addressSyncOracleXPub},
	))
	if err != nil {
		t.Fatal(err)
	}
	if account.GeneratorName != fixture.Generator || account.Receiving.ChainNumber != int64(ReceiveChain) ||
		account.Change.ChainNumber != int64(ChangeChain) ||
		!reflect.DeepEqual(account.Receiving.Gap, fixture.ReceivingGap) ||
		!reflect.DeepEqual(account.Change.Gap, fixture.ChangeGap) ||
		!reflect.DeepEqual(account.Receiving.MaximumUsesPerAddress, fixture.ReceivingMaximumUses) ||
		!reflect.DeepEqual(account.Change.MaximumUsesPerAddress, fixture.ChangeMaximumUses) {
		t.Fatalf("Go account address defaults differ: receiving %+v change %+v", account.Receiving, account.Change)
	}
}

func assertAddressSyncOracleFailures(t *testing.T, oracle addressSyncOracleResponse) {
	t.Helper()
	lock := oracle.Addresses.LockGuard
	if lock.ErrorType == nil || *lock.ErrorType != "RuntimeError" || lock.ErrorMessage == nil ||
		*lock.ErrorMessage != "Should not be called outside of address_generator_lock." {
		t.Fatalf("lock guard = %+v", lock)
	}
	partial := oracle.Addresses.PartialAnnouncementFailure
	if partial.ErrorType == nil || *partial.ErrorType != "ProbeAnnouncementError" ||
		partial.ErrorMessage == nil || *partial.ErrorMessage != "controlled announcement failure" ||
		len(partial.Persisted) != 2 || len(partial.Announcements) != 1 ||
		len(partial.RetryCreated) != 0 ||
		!reflect.DeepEqual(partial.Announcements[0].Addresses, []string{"address-0-0", "address-0-1"}) {
		t.Fatalf("partial announcement failure = %+v", partial)
	}
	single := oracle.Addresses.SingleAddress
	history := "a:1:b:2:c:3:"
	wantRecord := []addressSyncOracleRecord{{
		Address: "address-root", Chain: ReceiveChain, N: 0, History: &history, UsedTimes: 3,
	}}
	if !single.SameManager || !reflect.DeepEqual(single.FirstCreated, []string{"address-root"}) ||
		len(single.SecondCreated) != 0 || single.UsableAfterThreeUses != "address-root" ||
		!reflect.DeepEqual(single.Records, wantRecord) ||
		!reflect.DeepEqual(single.Announcements, []addressSyncOracleAnnouncement{{
			Chain: ReceiveChain, Addresses: []string{"address-root"},
		}}) {
		t.Fatalf("single-address fixture = %+v", single)
	}
}

func addressSyncOracleGapPlan(records []addressSyncOracleRecord, gap int) (start, count int) {
	recent := append([]addressSyncOracleRecord(nil), records...)
	sort.SliceStable(recent, func(left, right int) bool { return recent[left].N > recent[right].N })
	if len(recent) > gap {
		recent = recent[:gap]
	}
	existingGap := 0
	for _, record := range recent {
		if record.UsedTimes != 0 {
			break
		}
		existingGap++
	}
	if existingGap == gap {
		return 0, 0
	}
	if len(recent) > 0 {
		start = recent[0].N + 1
	}
	return start, gap - existingGap
}

func addressSyncOracleOrder(
	records []addressSyncOracleRecord, maximumUses *int,
) []addressSyncOracleRecord {
	ordered := make([]addressSyncOracleRecord, 0, len(records))
	for _, record := range records {
		if maximumUses == nil || record.UsedTimes < *maximumUses {
			ordered = append(ordered, record)
		}
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].UsedTimes != ordered[right].UsedTimes {
			return ordered[left].UsedTimes < ordered[right].UsedTimes
		}
		return ordered[left].N < ordered[right].N
	})
	return ordered
}

func addressSyncOracleMaxGap(records []addressSyncOracleRecord) int {
	ordered := append([]addressSyncOracleRecord(nil), records...)
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].N < ordered[right].N })
	maximum, current := 0, 0
	for _, record := range ordered {
		if record.UsedTimes == 0 {
			current++
		} else {
			maximum = max(maximum, current)
			current = 0
		}
	}
	// Python intentionally does not fold the trailing run into maximum.
	return maximum
}

func runAddressSyncOracle(t *testing.T) addressSyncOracleResponse {
	t.Helper()
	sdkRoot, script := addressSyncOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python address sync oracle failed: %v\n%s", err, output)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var oracle addressSyncOracleResponse
	if err := decoder.Decode(&oracle); err != nil {
		t.Fatalf("decode address sync oracle: %v\n%s", err, output)
	}
	return oracle
}

func addressSyncOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate address sync oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "address_sync_oracle.py")
	for relative := range addressSyncOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local address sync source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("address sync oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	return sdkRoot, script
}
