package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionHistoryHydratesStoredSignedClaimChannel(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	fixture.store(t, fixture.channel, []uint32{0}, nil, nil)
	fixture.store(t, fixture.stream, []uint32{0}, nil, nil)

	transaction := fixture.get(t, fixture.stream)
	output := &transaction.Outputs[0]
	channel := output.Channel
	if channel == nil {
		t.Fatal("stored signed claim channel = nil")
	}
	if channel == &fixture.channel.Outputs[0] {
		t.Fatal("stored signed claim reused the in-memory fixture channel")
	}
	claimID, err := channel.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	if claimID != fixture.channelID || channel.ID() != fixture.channel.Outputs[0].ID() ||
		channel.Amount != fixture.channel.Outputs[0].Amount || channel.owner == nil ||
		channel.owner.ID != fixture.channel.ID || channel.owner.Height != fixture.channel.Height {
		t.Fatalf("hydrated channel = %#v / parent %#v", channel, channel.owner)
	}
	publicKey, isChannel, err := DecodeChannelClaimPublicKey(channel.Script.Source)
	if err != nil || !isChannel ||
		!bytes.Equal(publicKey, fixture.channelKey.PublicKey().CompressedBytes()) {
		t.Fatalf("hydrated channel public key = %x, %t, %v", publicKey, isChannel, err)
	}
	if output.PrivateKey != nil || channel.PrivateKey != nil || channel.Channel != nil {
		t.Fatalf("walletless hydration annotations = output key %v, channel key %v, parent %v",
			output.PrivateKey, channel.PrivateKey, channel.Channel)
	}

	fixture.ledger.Headers = newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	encoded, err := fixture.ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	outputs, ok := encoded["outputs"].([]any)
	if !ok || len(outputs) != 1 {
		t.Fatalf("encoded signed claim outputs = %#v", encoded["outputs"])
	}
	stream, ok := outputs[0].(map[string]any)
	if !ok || stream["is_channel_signature_valid"] != true {
		t.Fatalf("encoded signed claim = %#v", outputs[0])
	}
	signingChannel, ok := stream["signing_channel"].(map[string]any)
	if !ok || signingChannel["txid"] != fixture.channel.ID ||
		signingChannel["nout"] != uint32(0) || signingChannel["claim_id"] != fixture.channelID ||
		signingChannel["name"] != "@local" || signingChannel["value_type"] != "channel" ||
		signingChannel["has_signing_key"] != false {
		t.Fatalf("encoded full signing channel = %#v", stream["signing_channel"])
	}
	channelValue, ok := signingChannel["value"].(map[string]any)
	if !ok || channelValue["public_key"] == nil || channelValue["public_key_id"] == nil {
		t.Fatalf("encoded signing channel value = %#v", signingChannel["value"])
	}
}

func TestTransactionHistorySignedClaimChannelLookupBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *transactionChannelHydrationFixture)
	}{
		{
			name: "missing",
			prepare: func(t *testing.T, fixture *transactionChannelHydrationFixture) {
				fixture.store(t, fixture.channel, nil, nil, nil)
			},
		},
		{
			name: "spent",
			prepare: func(t *testing.T, fixture *transactionChannelHydrationFixture) {
				fixture.store(t, fixture.channel, []uint32{0}, nil, nil)
				spendInput, err := NewSpendInput(&fixture.channel.Outputs[0])
				if err != nil {
					t.Fatal(err)
				}
				spend := transactionChannelHydrationTransaction(
					t, 0x7301, []TransactionInput{spendInput},
					NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x73}, 20)),
				)
				fixture.store(t, spend, []uint32{0}, nil, []ledgerdb.TransactionInputRow{{
					TXOID: fixture.channel.Outputs[0].ID(), Position: 0,
				}})
			},
		},
		{
			name: "reserved",
			prepare: func(t *testing.T, fixture *transactionChannelHydrationFixture) {
				fixture.store(t, fixture.channel, []uint32{0}, nil, nil)
				if err := fixture.ledger.Database.ReserveOutputs(
					context.Background(), []string{fixture.channel.Outputs[0].ID()}, true,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrong stored type",
			prepare: func(t *testing.T, fixture *transactionChannelHydrationFixture) {
				fixture.store(t, fixture.channel, []uint32{0}, map[uint32]int64{
					0: TransactionOutputTypeStream,
				}, nil)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTransactionChannelHydrationFixture(t)
			test.prepare(t, fixture)
			fixture.store(t, fixture.stream, []uint32{0}, nil, nil)

			output := &fixture.get(t, fixture.stream).Outputs[0]
			if output.Channel != nil || output.PrivateKey != nil {
				t.Fatalf("unavailable channel annotations = channel %#v, private key %v",
					output.Channel, output.PrivateKey)
			}
		})
	}
}

func TestTransactionHistoryLeavesSignedSupportChannelNil(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	message := protowire.AppendTag(nil, 2, protowire.BytesType)
	message = protowire.AppendBytes(message, []byte("local support"))
	signed := transactionChannelHydrationSignedPayload(
		t, fixture.channelKey, fixture.channelHash, [sha256.Size]byte{}, message,
	)
	support, err := NewSupportDataOutput(
		2, "supported", strings.Repeat("11", 20), signed, bytes.Repeat([]byte{0x74}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	supportTransaction := transactionChannelHydrationTransaction(
		t, 0x7401, nil, support,
	)
	fixture.store(t, fixture.channel, []uint32{0}, nil, nil)
	fixture.store(t, supportTransaction, []uint32{0}, nil, nil)

	output := &fixture.get(t, supportTransaction).Outputs[0]
	decoded, err := DecodeSupportValue(output.Script.Support)
	if err != nil || !decoded.IsSigned() {
		t.Fatalf("support fixture = %#v, %v", decoded, err)
	}
	if output.Channel != nil || output.PrivateKey != nil {
		t.Fatalf("signed support annotations = channel %#v, private key %v",
			output.Channel, output.PrivateKey)
	}
}

func TestTransactionHistoryDoesNotHydrateUnstoredRawClaimOutput(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	unstoredClaim := fixture.signedStreamOutput(t, 3, "unstored", 0x75)
	transaction := transactionChannelHydrationTransaction(
		t, 0x7501, nil,
		NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x75}, 20)),
		unstoredClaim,
	)
	fixture.store(t, fixture.channel, []uint32{0}, nil, nil)
	fixture.store(t, transaction, []uint32{0}, nil, nil)

	hydrated := fixture.get(t, transaction)
	if len(hydrated.Outputs) != 2 || hydrated.Outputs[1].Channel != nil ||
		hydrated.Outputs[1].PrivateKey != nil || hydrated.Outputs[1].IsMyOutput == nil ||
		*hydrated.Outputs[1].IsMyOutput {
		t.Fatalf("unstored parsed output = %#v", hydrated.Outputs[1])
	}
}

func TestTransactionHistoryHydratesResolvedInputChannel(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	fixture.store(t, fixture.channel, []uint32{0}, nil, nil)
	fixture.store(t, fixture.stream, []uint32{0}, nil, nil)
	spendInput, err := NewSpendInput(&fixture.stream.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	spend := transactionChannelHydrationTransaction(
		t, 0x7601, []TransactionInput{spendInput},
		NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x76}, 20)),
	)
	fixture.store(t, spend, []uint32{0}, nil, []ledgerdb.TransactionInputRow{{
		TXOID: fixture.stream.Outputs[0].ID(), Position: 0,
	}})

	transaction := fixture.get(t, spend)
	if len(transaction.Inputs) != 1 || transaction.Inputs[0].ResolvedOutput == nil {
		t.Fatalf("resolved input = %#v", transaction.Inputs)
	}
	resolved := transaction.Inputs[0].ResolvedOutput
	if resolved.Channel == nil || resolved.Channel.ID() != fixture.channel.Outputs[0].ID() ||
		resolved.Channel.PrivateKey != nil || resolved.PrivateKey != nil {
		t.Fatalf("resolved signed output annotations = %#v / channel %#v",
			resolved, resolved.Channel)
	}
	fixture.ledger.Headers = newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	encoded, err := fixture.ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	inputs := encoded["inputs"].([]any)
	encodedInput := inputs[0].(map[string]any)
	if _, exists := encodedInput["value"]; !exists {
		t.Fatalf("resolved signed input lost claim value: %#v", encodedInput)
	}
	for _, absent := range []string{"signing_channel", "is_channel_signature_valid"} {
		if _, exists := encodedInput[absent]; exists {
			t.Fatalf("resolved signed input contains %s: %#v", absent, encodedInput)
		}
	}
}

func TestTransactionHistoryRejectsSigningChannelCycle(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	firstID := strings.Repeat("a1", 20)
	secondID := strings.Repeat("b2", 20)
	channelMessage := makeV2ChannelClaim(fixture.channelKey.PublicKey().CompressedBytes())[1:]

	makeUpdate := func(nonce uint32, ownID, parentID string) *Transaction {
		t.Helper()
		parentHash, err := decodeTransactionClaimID(parentID)
		if err != nil {
			t.Fatal(err)
		}
		claim := transactionChannelHydrationSignedPayload(
			t, fixture.channelKey, parentHash, [sha256.Size]byte{}, channelMessage,
		)
		output, err := NewUpdateClaimOutput(
			5, "@cycle", ownID, claim, bytes.Repeat([]byte{0x77}, 20),
		)
		if err != nil {
			t.Fatal(err)
		}
		return transactionChannelHydrationTransaction(t, nonce, nil, output)
	}
	first := makeUpdate(0x7701, firstID, secondID)
	second := makeUpdate(0x7702, secondID, firstID)
	fixture.store(t, first, []uint32{0}, nil, nil)
	fixture.store(t, second, []uint32{0}, nil, nil)

	transactionID := first.ID
	transactions, err := fixture.ledger.GetTransactions(context.Background(), TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &transactionID},
	})
	if !errors.Is(err, ErrTransactionChannelHydrationCycle) || transactions != nil ||
		(!strings.Contains(err.Error(), firstID) && !strings.Contains(err.Error(), secondID)) {
		t.Fatalf("channel cycle = transactions %#v, error %v", transactions, err)
	}
}

type transactionChannelHydrationFixture struct {
	ledger      *Ledger
	channelKey  *keys.PrivateKey
	channel     *Transaction
	stream      *Transaction
	channelID   string
	channelHash []byte
}

func newTransactionChannelHydrationFixture(t *testing.T) *transactionChannelHydrationFixture {
	t.Helper()
	database, err := ledgerdb.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close channel hydration database: %v", err)
		}
	})
	privateKey, err := keys.PrivateKeyFromSeed(keys.MainNet, bytes.Repeat([]byte{0x71}, 32))
	if err != nil {
		t.Fatal(err)
	}
	pubKeyHash := keys.Hash160(privateKey.PublicKey().CompressedBytes())
	channel := transactionChannelHydrationTransaction(
		t, 0x7101, nil,
		NewClaimNameOutput(
			10, "@local", makeV2ChannelClaim(privateKey.PublicKey().CompressedBytes()), pubKeyHash[:],
		),
	)
	channelID, err := channel.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	channelHash, err := decodeTransactionClaimID(channelID)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &transactionChannelHydrationFixture{
		ledger:     &Ledger{Network: keys.MainNet, Database: database},
		channelKey: privateKey, channel: channel, channelID: channelID,
		channelHash: channelHash,
	}
	fixture.stream = transactionChannelHydrationTransaction(
		t, 0x7201, nil, fixture.signedStreamOutput(t, 4, "stored", 0x72),
	)
	fixture.channel.Height, fixture.channel.Position, fixture.channel.IsVerified = 101, 1, true
	fixture.stream.Height, fixture.stream.Position, fixture.stream.IsVerified = 102, 2, true
	return fixture
}

func (fixture *transactionChannelHydrationFixture) signedStreamOutput(
	t *testing.T, amount uint64, name string, pubKeyHashByte byte,
) TransactionOutput {
	t.Helper()
	message := protowire.AppendTag(nil, 1, protowire.BytesType)
	message = protowire.AppendBytes(message, nil)
	claim := transactionChannelHydrationSignedPayload(
		t, fixture.channelKey, fixture.channelHash, [sha256.Size]byte{}, message,
	)
	return NewClaimNameOutput(
		amount, name, claim, bytes.Repeat([]byte{pubKeyHashByte}, 20),
	)
}

func (fixture *transactionChannelHydrationFixture) store(
	t *testing.T,
	transaction *Transaction,
	outputPositions []uint32,
	typeOverrides map[uint32]int64,
	inputs []ledgerdb.TransactionInputRow,
) {
	t.Helper()
	metadata := ProjectTransactionMetadata(transaction)
	row := ledgerdb.TransactionIORow{
		Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
			Height: transaction.Height, Position: transaction.Position,
			IsVerified: transaction.IsVerified,
		},
		Inputs: append([]ledgerdb.TransactionInputRow(nil), inputs...),
	}
	for _, position := range outputPositions {
		if uint64(position) >= uint64(len(transaction.Outputs)) {
			t.Fatalf("fixture output position %d is out of range", position)
		}
		output := &transaction.Outputs[position]
		projected := metadata.Outputs[position]
		if projected.Err != nil {
			t.Fatal(projected.Err)
		}
		address, err := output.Address(fixture.ledger.Network)
		if err != nil {
			t.Fatal(err)
		}
		outputType := projected.TXOType
		if overridden, ok := typeOverrides[position]; ok {
			outputType = overridden
		}
		row.Outputs = append(row.Outputs, ledgerdb.TransactionOutputRow{
			TXOID: output.ID(), Address: &address, Position: int64(position),
			Amount: int64(output.Amount), Script: append([]byte(nil), output.Script.Source...),
			TXOType: outputType, ClaimID: projected.ClaimID,
			ClaimName: projected.ClaimName, HasSource: projected.HasSource,
			ChannelID: projected.ChannelID, RepostedClaimID: projected.RepostedClaimID,
		})
	}
	if err := fixture.ledger.Database.SaveTransactionIOBatch(
		context.Background(), []ledgerdb.TransactionIORow{row}, "", "",
	); err != nil {
		t.Fatal(err)
	}
}

func (fixture *transactionChannelHydrationFixture) get(
	t *testing.T, transaction *Transaction,
) *Transaction {
	t.Helper()
	transactionID := transaction.ID
	transactions, err := fixture.ledger.GetTransactions(context.Background(), TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &transactionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 1 {
		t.Fatalf("transactions for %s = %d, want 1", transactionID, len(transactions))
	}
	return transactions[0]
}

func transactionChannelHydrationTransaction(
	t *testing.T, nonce uint32, inputs []TransactionInput, outputs ...TransactionOutput,
) *Transaction {
	t.Helper()
	transaction := NewTransaction()
	transaction.LockTime = nonce
	if len(inputs) == 0 {
		inputs = []TransactionInput{{
			PreviousIndex: math.MaxUint32, Sequence: math.MaxUint32,
			Coinbase: []byte{byte(nonce), byte(nonce >> 8), byte(nonce >> 16), byte(nonce >> 24)},
		}}
	}
	transaction.AddInputs(inputs)
	transaction.AddOutputs(outputs)
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	return transaction
}

func transactionChannelHydrationSignedPayload(
	t *testing.T,
	privateKey *keys.PrivateKey,
	signingHash []byte,
	firstInputHash [sha256.Size]byte,
	message []byte,
) []byte {
	t.Helper()
	digestMaterial := make([]byte, 0, len(firstInputHash)+4+len(signingHash)+len(message))
	digestMaterial = append(digestMaterial, firstInputHash[:]...)
	var previousIndex [4]byte
	binary.LittleEndian.PutUint32(previousIndex[:], math.MaxUint32)
	digestMaterial = append(digestMaterial, previousIndex[:]...)
	digestMaterial = append(digestMaterial, signingHash...)
	digestMaterial = append(digestMaterial, message...)
	digest := sha256.Sum256(digestMaterial)
	signature, err := privateKey.SignCompact(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 0, 1+len(signingHash)+len(signature)+len(message))
	payload = append(payload, 1)
	payload = append(payload, signingHash...)
	payload = append(payload, signature...)
	return append(payload, message...)
}
