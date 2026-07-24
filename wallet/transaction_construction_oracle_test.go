package wallet

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type transactionConstructionOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion                string `json:"python_version"`
		UnsignedConstructionExecuted bool   `json:"unsigned_construction_executed"`
		FixtureSource                string `json:"fixture_source"`
	} `json:"metadata"`
	UnsignedConstruction transactionOracleUnsignedConstruction `json:"unsigned_construction"`
}

type transactionOracleUnsignedConstruction struct {
	Defaults         []transactionOracleConstructedTransaction `json:"defaults"`
	GeneratedOutputs []transactionOracleGeneratedOutput        `json:"generated_outputs"`
	SpendPlaceholder transactionOracleSpendPlaceholder         `json:"spend_placeholder"`
	Chained          transactionOracleConstructedTransaction   `json:"chained"`
	MutationReset    transactionOracleMutationReset            `json:"mutation_reset"`
	CanonicalResets  []transactionOracleCanonicalReset         `json:"canonical_resets"`
}

type transactionOracleConstructedTransaction struct {
	Name                string   `json:"name"`
	Version             uint32   `json:"version"`
	LockTime            uint32   `json:"locktime"`
	Height              int64    `json:"height"`
	Position            int64    `json:"position"`
	IsVerified          bool     `json:"is_verified"`
	RawHex              string   `json:"raw_hex"`
	RawSansSegWitHex    string   `json:"raw_sans_segwit_hex"`
	HashHex             string   `json:"hash_hex"`
	ID                  string   `json:"id"`
	Size                int      `json:"size"`
	BaseSize            int      `json:"base_size"`
	InputSum            uint64   `json:"input_sum"`
	OutputSum           uint64   `json:"output_sum"`
	Fee                 int64    `json:"fee"`
	InputPositions      []uint32 `json:"input_positions"`
	OutputPositions     []uint32 `json:"output_positions"`
	InputTransactionIDs []string `json:"input_transaction_ids"`
	InputPreviousIDs    []string `json:"input_previous_ids"`
	OutputIDs           []string `json:"output_ids"`
	InputSizes          []int    `json:"input_sizes"`
	OutputSizes         []int    `json:"output_sizes"`
	ReturnedSelf        bool     `json:"returned_self"`
}

type transactionOracleGeneratedOutput struct {
	Name          string                  `json:"name"`
	Amount        uint64                  `json:"amount"`
	Size          int                     `json:"size"`
	SerializedHex string                  `json:"serialized_hex"`
	Script        transactionOracleScript `json:"script"`
}

type transactionOracleSpendPlaceholder struct {
	ParentRawHex               string                  `json:"parent_raw_hex"`
	ParentID                   string                  `json:"parent_id"`
	ParentOutputID             string                  `json:"parent_output_id"`
	PreviousTransactionHashHex string                  `json:"previous_transaction_hash_hex"`
	PreviousOutputHashHex      string                  `json:"previous_output_hash_hex"`
	PreviousIndex              uint32                  `json:"previous_index"`
	Position                   *uint32                 `json:"position"`
	Sequence                   uint32                  `json:"sequence"`
	Amount                     uint64                  `json:"amount"`
	Size                       int                     `json:"size"`
	SerializedHex              string                  `json:"serialized_hex"`
	Script                     transactionOracleScript `json:"script"`
}

type transactionOracleMutationReset struct {
	InitialAmount      uint64 `json:"initial_amount"`
	MutatedAmount      uint64 `json:"mutated_amount"`
	InitialRawHex      string `json:"initial_raw_hex"`
	CachedRawHex       string `json:"cached_raw_hex"`
	ResetRawHex        string `json:"reset_raw_hex"`
	InitialID          string `json:"initial_id"`
	CachedID           string `json:"cached_id"`
	ResetID            string `json:"reset_id"`
	InitialOutputID    string `json:"initial_output_id"`
	CachedOutputID     string `json:"cached_output_id"`
	ResetOutputID      string `json:"reset_output_id"`
	CachedRawUnchanged bool   `json:"cached_raw_unchanged"`
	CachedIDUnchanged  bool   `json:"cached_id_unchanged"`
	ResetRawChanged    bool   `json:"reset_raw_changed"`
	ResetIDChanged     bool   `json:"reset_id_changed"`
}

type transactionOracleCanonicalReset struct {
	Name            string `json:"name"`
	OriginalRawHex  string `json:"original_raw_hex"`
	OriginalID      string `json:"original_id"`
	CanonicalRawHex string `json:"canonical_raw_hex"`
	CanonicalID     string `json:"canonical_id"`
	TrailingHex     string `json:"trailing_hex"`
	RawChanged      bool   `json:"raw_changed"`
	IDChanged       bool   `json:"id_changed"`
}

func TestUnsignedTransactionConstructionPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionConstructionOracle(t)
	if oracle.Reference.Commit != transactionOraclePinnedCommit ||
		oracle.Reference.Version != transactionOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionOraclePinnedSources) {
		t.Fatalf("transaction construction oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.UnsignedConstructionExecuted ||
		oracle.Metadata.FixtureSource != "tests/unit/wallet/test_transaction.py" {
		t.Fatalf("transaction construction oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && oracle.Metadata.PythonVersion != want {
		t.Fatalf("transaction construction oracle Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}

	construction := oracle.UnsignedConstruction
	assertTransactionConstructionDefaults(t, construction.Defaults)
	assertTransactionConstructionOutputs(t, construction.GeneratedOutputs)
	assertTransactionSpendPlaceholder(t, construction.SpendPlaceholder)
	assertTransactionChainedConstruction(t, construction.Chained)
	assertTransactionMutationReset(t, construction.MutationReset)
	assertTransactionCanonicalResets(t, construction.CanonicalResets)
	assertGoUnsignedConstructionMatchesOracle(t, construction)
}

func assertGoUnsignedConstructionMatchesOracle(
	t *testing.T, expected transactionOracleUnsignedConstruction,
) {
	t.Helper()
	defaultTransaction := NewTransaction()
	customTransaction := NewTransaction()
	customTransaction.Version = 2
	customTransaction.LockTime = 42
	customTransaction.ResetDerived()
	if err := customTransaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	actualDefaults := []transactionOracleConstructedTransaction{
		summarizeGoConstructedTransaction("default", defaultTransaction),
		summarizeGoConstructedTransaction("version and locktime", customTransaction),
	}
	if !reflect.DeepEqual(actualDefaults, expected.Defaults) {
		t.Fatalf("Go constructed defaults = %+v, pinned Python %+v", actualDefaults, expected.Defaults)
	}

	pubKeyHash := bytes.Repeat([]byte{0x11}, 20)
	scriptHash := bytes.Repeat([]byte{0x22}, 20)
	claimID := strings.Repeat("33", 20)
	claim := []byte{0x01, 0x02, 0x03}
	support := []byte{0x04, 0x05}
	update, err := NewUpdateClaimOutput(4_004, "name", claimID, claim, pubKeyHash)
	if err != nil {
		t.Fatal(err)
	}
	supportData, err := NewSupportDataOutput(5_005, "name", claimID, support, pubKeyHash)
	if err != nil {
		t.Fatal(err)
	}
	actualOutputs := []TransactionOutput{
		NewPayPubKeyHashOutput(1_001, pubKeyHash),
		NewPayScriptHashOutput(2_002, scriptHash),
		NewReturnDataOutput([]byte{0xaa, 0xbb}),
		NewClaimNameOutput(3_003, "name", claim, pubKeyHash),
		update,
		supportData,
	}
	for index, output := range actualOutputs {
		want := expected.GeneratedOutputs[index]
		if output.Amount != want.Amount || output.Size() != want.Size ||
			hex.EncodeToString(serializeGoTransactionOutput(output)) != want.SerializedHex {
			t.Fatalf("Go generated output %d = %#v, pinned Python %+v", index, output, want)
		}
		assertTransactionOracleOutputScript(t, want.Script, output.Script)
	}

	assertGoSpendPlaceholderMatchesOracle(t, expected.SpendPlaceholder)
	assertGoChainedConstructionMatchesOracle(t, expected.Chained)
	assertGoMutationResetMatchesOracle(t, expected.MutationReset)
	assertGoCanonicalResetsMatchOracle(t, expected.CanonicalResets)
}

func assertGoSpendPlaceholderMatchesOracle(
	t *testing.T, expected transactionOracleSpendPlaceholder,
) {
	t.Helper()
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(123_456_789, bytes.Repeat([]byte{0x44}, 20)),
	})
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	var previousOutputHash bytes.Buffer
	previousOutputHash.Write(input.PreviousHash[:])
	_ = binary.Write(&previousOutputHash, binary.BigEndian, input.PreviousIndex)
	if hex.EncodeToString(parent.Raw) != expected.ParentRawHex || parent.ID != expected.ParentID ||
		parent.Outputs[0].ID() != expected.ParentOutputID ||
		hex.EncodeToString(input.PreviousHash[:]) != expected.PreviousTransactionHashHex ||
		hex.EncodeToString(previousOutputHash.Bytes()) != expected.PreviousOutputHashHex ||
		input.PreviousIndex != expected.PreviousIndex || input.Sequence != expected.Sequence ||
		input.ResolvedOutput == nil || input.ResolvedOutput.Amount != expected.Amount ||
		input.Size() != expected.Size ||
		hex.EncodeToString(serializeGoTransactionInput(input)) != expected.SerializedHex {
		t.Fatalf("Go spend placeholder = parent %#v input %#v, pinned Python %+v", parent, input, expected)
	}
	assertTransactionOracleInputScript(t, expected.Script, input.Script)
}

func assertGoChainedConstructionMatchesOracle(
	t *testing.T, expected transactionOracleConstructedTransaction,
) {
	t.Helper()
	firstParent := NewTransaction()
	firstParent.Version = 2
	firstParent.AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(700_000, bytes.Repeat([]byte{0x51}, 20)),
	})
	secondParent := NewTransaction()
	secondParent.Version = 3
	secondParent.AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(900_000, bytes.Repeat([]byte{0x52}, 20)),
	})
	firstInput, err := NewSpendInput(&firstParent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	secondInput, err := NewSpendInput(&secondParent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction()
	transaction.Version = 4
	transaction.LockTime = 77
	returned := transaction.
		AddInputs([]TransactionInput{firstInput}).
		AddOutputs([]TransactionOutput{
			NewPayPubKeyHashOutput(500_000, bytes.Repeat([]byte{0x61}, 20)),
		}).
		AddInputs([]TransactionInput{secondInput}).
		AddOutputs([]TransactionOutput{
			NewPayScriptHashOutput(1_000_000, bytes.Repeat([]byte{0x62}, 20)),
		})
	actual := summarizeGoConstructedTransaction("chained add", transaction)
	actual.ReturnedSelf = returned == transaction
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Go chained construction = %+v, pinned Python %+v", actual, expected)
	}
}

func assertGoMutationResetMatchesOracle(t *testing.T, expected transactionOracleMutationReset) {
	t.Helper()
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(800_000, bytes.Repeat([]byte{0x71}, 20)),
	})
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction().
		AddInputs([]TransactionInput{input}).
		AddOutputs([]TransactionOutput{
			NewPayPubKeyHashOutput(700_000, bytes.Repeat([]byte{0x72}, 20)),
		})
	initialRaw := hex.EncodeToString(transaction.Raw)
	initialID := transaction.ID
	initialOutputID := transaction.Outputs[0].ID()
	initialAmount := transaction.Outputs[0].Amount
	transaction.Outputs[0].Amount++
	cachedRaw := hex.EncodeToString(transaction.Raw)
	cachedID := transaction.ID
	cachedOutputID := transaction.Outputs[0].ID()
	transaction.ResetDerived()
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	actual := transactionOracleMutationReset{
		InitialAmount: initialAmount, MutatedAmount: transaction.Outputs[0].Amount,
		InitialRawHex: initialRaw, CachedRawHex: cachedRaw,
		ResetRawHex: hex.EncodeToString(transaction.Raw),
		InitialID:   initialID, CachedID: cachedID, ResetID: transaction.ID,
		InitialOutputID: initialOutputID, CachedOutputID: cachedOutputID,
		ResetOutputID:      transaction.Outputs[0].ID(),
		CachedRawUnchanged: cachedRaw == initialRaw,
		CachedIDUnchanged:  cachedID == initialID,
		ResetRawChanged:    hex.EncodeToString(transaction.Raw) != initialRaw,
		ResetIDChanged:     transaction.ID != initialID,
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Go mutation/reset = %+v, pinned Python %+v", actual, expected)
	}
}

func assertGoCanonicalResetsMatchOracle(
	t *testing.T, expected []transactionOracleCanonicalReset,
) {
	t.Helper()
	for index, want := range expected {
		raw := transactionOracleDecodeHex(t, want.OriginalRawHex)
		transaction, err := ParseTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		actual := transactionOracleCanonicalReset{
			Name: want.Name, OriginalRawHex: hex.EncodeToString(transaction.Raw),
			OriginalID: transaction.ID, TrailingHex: hex.EncodeToString(transaction.Trailing),
		}
		transaction.ResetDerived()
		if err := transaction.RebuildDerived(); err != nil {
			t.Fatal(err)
		}
		actual.CanonicalRawHex = hex.EncodeToString(transaction.Raw)
		actual.CanonicalID = transaction.ID
		actual.RawChanged = actual.CanonicalRawHex != actual.OriginalRawHex
		actual.IDChanged = actual.CanonicalID != actual.OriginalID
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("Go canonical reset %d = %+v, pinned Python %+v", index, actual, want)
		}
	}
}

func summarizeGoConstructedTransaction(
	name string, transaction *Transaction,
) transactionOracleConstructedTransaction {
	result := transactionOracleConstructedTransaction{
		Name: name, Version: transaction.Version, LockTime: transaction.LockTime,
		Height: transaction.Height, Position: transaction.Position,
		IsVerified: transaction.IsVerified, RawHex: hex.EncodeToString(transaction.Raw),
		RawSansSegWitHex: hex.EncodeToString(transaction.RawSansSegWit),
		HashHex:          hex.EncodeToString(transaction.Hash[:]), ID: transaction.ID,
		Size: transaction.Size(), BaseSize: transaction.BaseSize(),
		InputSum: transaction.InputSum(), OutputSum: transaction.OutputSum(), Fee: transaction.Fee(),
		InputPositions:      make([]uint32, 0, len(transaction.Inputs)),
		OutputPositions:     make([]uint32, 0, len(transaction.Outputs)),
		InputTransactionIDs: make([]string, 0, len(transaction.Inputs)),
		InputPreviousIDs:    make([]string, 0, len(transaction.Inputs)),
		OutputIDs:           make([]string, 0, len(transaction.Outputs)),
		InputSizes:          make([]int, 0, len(transaction.Inputs)),
		OutputSizes:         make([]int, 0, len(transaction.Outputs)),
	}
	for _, input := range transaction.Inputs {
		result.InputPositions = append(result.InputPositions, input.Position)
		result.InputTransactionIDs = append(result.InputTransactionIDs, input.TransactionID())
		result.InputPreviousIDs = append(result.InputPreviousIDs, input.PreviousOutputID())
		result.InputSizes = append(result.InputSizes, input.Size())
	}
	for _, output := range transaction.Outputs {
		result.OutputPositions = append(result.OutputPositions, output.Position)
		result.OutputIDs = append(result.OutputIDs, output.ID())
		result.OutputSizes = append(result.OutputSizes, output.Size())
	}
	return result
}

func serializeGoTransactionInput(input TransactionInput) []byte {
	buffer := bytes.NewBuffer(make([]byte, 0, input.Size()))
	buffer.Write(input.PreviousHash[:])
	_ = binary.Write(buffer, binary.LittleEndian, input.PreviousIndex)
	source := input.Script.Source
	if input.IsCoinbase() {
		source = input.Coinbase
	}
	writeTransactionVarBytes(buffer, source)
	_ = binary.Write(buffer, binary.LittleEndian, input.Sequence)
	return buffer.Bytes()
}

func serializeGoTransactionOutput(output TransactionOutput) []byte {
	buffer := bytes.NewBuffer(make([]byte, 0, output.Size()))
	_ = binary.Write(buffer, binary.LittleEndian, output.Amount)
	writeTransactionVarBytes(buffer, output.Script.Source)
	return buffer.Bytes()
}

func assertTransactionConstructionDefaults(
	t *testing.T, defaults []transactionOracleConstructedTransaction,
) {
	t.Helper()
	if len(defaults) != 2 {
		t.Fatalf("constructed default count = %d, want 2", len(defaults))
	}
	wants := []struct {
		name     string
		version  uint32
		lockTime uint32
		raw      string
		id       string
	}{
		{"default", 1, 0, "01000000000000000000", "d21633ba23f70118185227be58a63527675641ad37967e2aa461559f577aec43"},
		{"version and locktime", 2, 42, "0200000000002a000000", "5c038dab49cd934dedc1d14bd7b1250be9d3cdb42ce23ce0c01aee157894a963"},
	}
	for index, want := range wants {
		got := defaults[index]
		if got.Name != want.name || got.Version != want.version || got.LockTime != want.lockTime ||
			got.RawHex != want.raw || got.RawSansSegWitHex != want.raw || got.ID != want.id ||
			got.Height != -2 || got.Position != -1 || got.IsVerified || got.Size != 10 ||
			got.BaseSize != 10 || got.InputSum != 0 || got.OutputSum != 0 || got.Fee != 0 ||
			len(got.InputPositions) != 0 || len(got.OutputPositions) != 0 {
			t.Fatalf("constructed default %d = %+v, want %+v", index, got, want)
		}
	}
}

func assertTransactionConstructionOutputs(
	t *testing.T, outputs []transactionOracleGeneratedOutput,
) {
	t.Helper()
	wants := []struct {
		name       string
		amount     uint64
		size       int
		template   string
		source     string
		serialized string
	}{
		{
			"pay pubkey hash", 1001, 34, TransactionScriptPayPubKeyHash,
			"76a914" + strings.Repeat("11", 20) + "88ac",
			"e9030000000000001976a914" + strings.Repeat("11", 20) + "88ac",
		},
		{
			"pay script hash", 2002, 32, TransactionScriptPayScriptHash,
			"a914" + strings.Repeat("22", 20) + "87",
			"d20700000000000017a914" + strings.Repeat("22", 20) + "87",
		},
		{"return data", 0, 13, TransactionScriptReturnData, "6a02aabb", "0000000000000000046a02aabb"},
		{
			"claim pubkey hash", 3003, 46, TransactionScriptClaimPubKeyHash,
			"b5046e616d65030102036d7576a914" + strings.Repeat("11", 20) + "88ac",
			"bb0b00000000000025b5046e616d65030102036d7576a914" + strings.Repeat("11", 20) + "88ac",
		},
		{
			"update pubkey hash", 4004, 67, TransactionScriptUpdatePubKey,
			"b7046e616d6514" + strings.Repeat("33", 20) + "030102036d6d76a914" + strings.Repeat("11", 20) + "88ac",
			"a40f0000000000003ab7046e616d6514" + strings.Repeat("33", 20) + "030102036d6d76a914" + strings.Repeat("11", 20) + "88ac",
		},
		{
			"support data pubkey hash", 5005, 66, TransactionScriptSupportDataKey,
			"b6046e616d6514" + strings.Repeat("33", 20) + "0204056d6d76a914" + strings.Repeat("11", 20) + "88ac",
			"8d1300000000000039b6046e616d6514" + strings.Repeat("33", 20) + "0204056d6d76a914" + strings.Repeat("11", 20) + "88ac",
		},
	}
	if len(outputs) != len(wants) {
		t.Fatalf("generated output count = %d, want %d", len(outputs), len(wants))
	}
	for index, want := range wants {
		got := outputs[index]
		if got.Name != want.name || got.Amount != want.amount || got.Size != want.size ||
			got.SerializedHex != want.serialized || !got.Script.OK ||
			got.Script.Template != want.template || got.Script.SourceHex != want.source {
			t.Fatalf("generated output %d = %+v, want %+v", index, got, want)
		}
	}
}

func assertTransactionSpendPlaceholder(t *testing.T, spend transactionOracleSpendPlaceholder) {
	t.Helper()
	signature := strings.Repeat("00", 72)
	publicKey := strings.Repeat("00", 33)
	script := "48" + signature + "21" + publicKey
	wantSerialized := spend.PreviousTransactionHashHex + "000000006b" + script + "ffffffff"
	if spend.ParentOutputID != spend.ParentID+":0" ||
		spend.PreviousOutputHashHex != spend.PreviousTransactionHashHex+"00000000" ||
		spend.PreviousIndex != 0 || spend.Position != nil || spend.Sequence != ^uint32(0) ||
		spend.Amount != 123456789 || spend.Size != 148 || spend.SerializedHex != wantSerialized ||
		!spend.Script.OK || spend.Script.Template != TransactionInputPubKeyHash ||
		spend.Script.SourceHex != script || spend.Script.Values["signature"] != signature ||
		spend.Script.Values["pubkey"] != publicKey {
		t.Fatalf("constructed spend placeholder = %+v", spend)
	}
}

func assertTransactionChainedConstruction(
	t *testing.T, transaction transactionOracleConstructedTransaction,
) {
	t.Helper()
	if transaction.Name != "chained add" || !transaction.ReturnedSelf ||
		transaction.Version != 4 || transaction.LockTime != 77 ||
		transaction.Height != -2 || transaction.Position != -1 || transaction.IsVerified ||
		transaction.RawHex != transaction.RawSansSegWitHex || transaction.Size != 372 ||
		transaction.BaseSize != 10 || transaction.InputSum != 1600000 ||
		transaction.OutputSum != 1500000 || transaction.Fee != 100000 ||
		!reflect.DeepEqual(transaction.InputPositions, []uint32{0, 1}) ||
		!reflect.DeepEqual(transaction.OutputPositions, []uint32{0, 1}) ||
		!reflect.DeepEqual(transaction.InputSizes, []int{148, 148}) ||
		!reflect.DeepEqual(transaction.OutputSizes, []int{34, 32}) {
		t.Fatalf("chained construction = %+v", transaction)
	}
	if !reflect.DeepEqual(
		transaction.InputTransactionIDs, []string{transaction.ID, transaction.ID},
	) || !reflect.DeepEqual(
		transaction.OutputIDs, []string{transaction.ID + ":0", transaction.ID + ":1"},
	) || len(transaction.InputPreviousIDs) != 2 ||
		!strings.HasSuffix(transaction.InputPreviousIDs[0], ":0") ||
		!strings.HasSuffix(transaction.InputPreviousIDs[1], ":0") ||
		transaction.InputPreviousIDs[0] == transaction.InputPreviousIDs[1] {
		t.Fatalf("chained parent-derived IDs = %+v", transaction)
	}
	if transaction.Size != transaction.BaseSize+
		transaction.InputSizes[0]+transaction.InputSizes[1]+
		transaction.OutputSizes[0]+transaction.OutputSizes[1] {
		t.Fatalf("chained size decomposition = %+v", transaction)
	}
}

func assertTransactionMutationReset(t *testing.T, mutation transactionOracleMutationReset) {
	t.Helper()
	if mutation.InitialAmount != 700000 || mutation.MutatedAmount != mutation.InitialAmount+1 ||
		!mutation.CachedRawUnchanged || !mutation.CachedIDUnchanged ||
		!mutation.ResetRawChanged || !mutation.ResetIDChanged ||
		mutation.CachedRawHex != mutation.InitialRawHex || mutation.CachedID != mutation.InitialID ||
		mutation.ResetRawHex == mutation.InitialRawHex || mutation.ResetID == mutation.InitialID ||
		mutation.InitialOutputID != mutation.InitialID+":0" ||
		mutation.CachedOutputID != mutation.CachedID+":0" ||
		mutation.ResetOutputID != mutation.ResetID+":0" {
		t.Fatalf("transaction mutation/reset = %+v", mutation)
	}
}

func assertTransactionCanonicalResets(
	t *testing.T, resets []transactionOracleCanonicalReset,
) {
	t.Helper()
	if len(resets) != 2 {
		t.Fatalf("canonical reset count = %d, want 2", len(resets))
	}
	canonicalRaw := resets[0].CanonicalRawHex
	canonicalID := resets[0].CanonicalID
	for index, reset := range resets {
		if !reset.RawChanged || !reset.IDChanged || reset.OriginalRawHex == reset.CanonicalRawHex ||
			reset.OriginalID == reset.CanonicalID || reset.CanonicalRawHex != canonicalRaw ||
			reset.CanonicalID != canonicalID {
			t.Fatalf("canonical reset %d = %+v", index, reset)
		}
	}
	if resets[0].Name != "noncanonical compact size" || resets[0].TrailingHex != "" ||
		resets[1].Name != "trailing bytes" || resets[1].TrailingHex != "deadbeef" ||
		!strings.HasSuffix(resets[1].OriginalRawHex, resets[1].TrailingHex) ||
		canonicalID != "b8211c82c3d15bcd78bba57005b86fed515149a53a425eb592c07af99fe559cc" {
		t.Fatalf("canonical reset fixtures = %+v", resets)
	}
}

func runTransactionConstructionOracle(t *testing.T) transactionConstructionOracleResponse {
	t.Helper()
	sdkRoot, script := transactionOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python transaction construction oracle failed: %v\n%s", err, output)
	}
	var oracle transactionConstructionOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction construction oracle: %v\n%s", err, output)
	}
	return oracle
}
