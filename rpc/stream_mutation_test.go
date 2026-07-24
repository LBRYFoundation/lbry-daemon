package rpc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	blobpkg "lbry/daemon/blob"
	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

type streamMutationAnalyzer struct {
	path  string
	spec  map[string]any
	err   error
	calls int
}

func (analyzer *streamMutationAnalyzer) Status(context.Context, bool, bool) map[string]any {
	return map[string]any{"available": true, "which": "fixture", "analyze_audio_volume": false}
}

func (analyzer *streamMutationAnalyzer) VerifyOrRepair(
	context.Context, bool, bool, string, bool,
) (string, map[string]any, error) {
	analyzer.calls++
	return analyzer.path, analyzer.spec, analyzer.err
}

func TestStreamCreatePreviewPublishesExistingDescriptor(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "stream_create", map[string]any{
		"name": "video", "bid": "1.0", "preview": true,
		"sd_hash": strings.Repeat("22", 48), "file_name": "video.mp4",
		"file_size": "123", "title": "Video", "width": 1280, "height": 720, "duration": 10,
	})
	encoded, ok := result.(map[string]any)
	if !ok || encoded["txid"] == "" {
		t.Fatalf("stream create = %#v", result)
	}
	output := encoded["outputs"].([]any)[0].(map[string]any)
	value := output["value"].(map[string]any)
	if output["value_type"] != "stream" || value["title"] != "Video" || value["stream_type"] != "video" {
		t.Fatalf("stream output = %#v", output)
	}
	spendable, err := fixture.ledger.Database.ListSpendableOutputs(context.Background(), []string{fixture.account.ID})
	if err != nil || len(spendable) != 1 {
		t.Fatalf("stream preview spendables = %#v, %v", spendable, err)
	}
}

func TestPublishRoutesNewNameToStreamCreate(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "publish", map[string]any{
		"name": "published-name", "bid": "1.0", "preview": true,
		"sd_hash": strings.Repeat("44", 48), "file_name": "document.pdf", "title": "Document",
	})
	output := result.(map[string]any)["outputs"].([]any)[0].(map[string]any)
	if output["value_type"] != "stream" || output["value"].(map[string]any)["title"] != "Document" {
		t.Fatalf("publish output = %#v", output)
	}
}

func TestPublishRoutesExistingStreamToSourcePreservingUpdate(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	_, claimID := persistStreamMutationFixture(t, &fixture, "existing")
	result := fileMutationRPCResult(t, fixture.server, "publish", map[string]any{
		"name": "existing", "title": "Replacement", "preview": true,
	})
	output := result.(map[string]any)["outputs"].([]any)[0].(map[string]any)
	value := output["value"].(map[string]any)
	source := value["source"].(map[string]any)
	if output["claim_op"] != "update" || output["claim_id"] != claimID ||
		value["title"] != "Replacement" || source["sd_hash"] != strings.Repeat("55", 48) ||
		source["name"] != "existing.mp4" || value["stream_type"] != "video" {
		t.Fatalf("publish update = %#v", output)
	}
}

func TestStreamCreatePublishesAndPersistsLocalFile(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.network.downloadComplete = nil
	controller := &fileControllerTest{}
	fixture.server.managedFileController = controller
	path := filepath.Join(t.TempDir(), "published.txt")
	if err := os.WriteFile(path, []byte("published content"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileMutationRPCResult(t, fixture.server, "stream_create", map[string]any{
		"name": "published", "bid": "1.0", "file_path": path,
	})
	fixture.network.mu.Lock()
	broadcasts := append([]string(nil), fixture.network.broadcasts...)
	fixture.network.mu.Unlock()
	if len(broadcasts) != 1 {
		t.Fatalf("stream broadcasts = %v", broadcasts)
	}
	transaction, err := walletpkg.ParseTransaction(mustDecodeHex(t, broadcasts[0]))
	if err != nil || len(transaction.Outputs) == 0 {
		t.Fatalf("published transaction = %#v, %v", transaction, err)
	}
	value, err := walletpkg.DecodeClaimValue(transaction.Outputs[0].Script.Claim)
	if err != nil {
		t.Fatal(err)
	}
	source := value.Value["source"].(map[string]any)
	sdHash, _ := source["sd_hash"].(string)
	if len(sdHash) != 96 {
		t.Fatalf("published source = %#v", source)
	}
	if len(controller.registered) != 1 || controller.registered[0] != sdHash {
		t.Fatalf("registered streams = %v", controller.registered)
	}
	descriptorBytes, ok := fixture.server.blobManager.Get(sdHash)
	if !ok {
		t.Fatalf("descriptor %s is unavailable", sdHash)
	}
	descriptor, err := blobpkg.ParseDescriptor(descriptorBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, content, err := fixture.server.blobManager.ReadStream(sdHash)
	if err != nil || string(content) != "published content" {
		t.Fatalf("published content = %q, %v", content, err)
	}
	rows, err := fixture.store.ListManagedFiles(context.Background())
	if err != nil || len(rows) != 1 || rows[0].SDHash != sdHash ||
		rows[0].StreamHash != descriptor.StreamHash || rows[0].ClaimOutpoint != transaction.Outputs[0].ID() {
		t.Fatalf("managed rows = %#v, %v", rows, err)
	}
}

func TestStreamCreatePublishesAnalyzedFileAndOverridesMediaSpec(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	original := filepath.Join(t.TempDir(), "original.mov")
	repaired := filepath.Join(t.TempDir(), "original_fixed.mp4")
	if err := os.WriteFile(original, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repaired, []byte("repaired media"), 0o600); err != nil {
		t.Fatal(err)
	}
	analyzer := &streamMutationAnalyzer{
		path: repaired, spec: map[string]any{"duration": 12, "width": 1920, "height": 1080},
	}
	fixture.server.fileAnalyzer = analyzer
	result := fileMutationRPCResult(t, fixture.server, "stream_create", map[string]any{
		"name": "optimized", "bid": "1.0", "file_path": original,
		"validate_file": true, "optimize_file": true,
		"duration": 1, "width": 2, "height": 3, "preview": true,
	})
	output := result.(map[string]any)["outputs"].([]any)[0].(map[string]any)
	value := output["value"].(map[string]any)
	source := value["source"].(map[string]any)
	video := value["video"].(map[string]any)
	if analyzer.calls != 1 || fmt.Sprint(video["duration"]) != "12" || fmt.Sprint(video["width"]) != "1920" ||
		fmt.Sprint(video["height"]) != "1080" || source["name"] != "original_fixed.mp4" ||
		fmt.Sprint(source["size"]) != "14" {
		t.Fatalf("analyzed stream = %#v, calls=%d", value, analyzer.calls)
	}
}

func TestStreamCreateStopsBeforeTransactionWhenValidationFails(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	path := filepath.Join(t.TempDir(), "invalid.mp4")
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.server.fileAnalyzer = &streamMutationAnalyzer{err: errors.New("Streamability verification failed")}
	params := map[string]any{
		"name": "invalid", "bid": "1.0", "file_path": path, "validate_file": true,
	}
	_, err := fixture.server.streamMutation(normalizedRPCParams{
		ctx: context.Background(), named: params, kwargs: params,
	}, false)
	if err == nil || !strings.Contains(strings.ToLower(fmt.Sprint(err)), "streamability") {
		t.Fatalf("validation failure = %v", err)
	}
	fixture.network.mu.Lock()
	broadcasts := len(fixture.network.broadcasts)
	fixture.network.mu.Unlock()
	if broadcasts != 0 {
		t.Fatalf("validation failure broadcast %d transactions", broadcasts)
	}
}

func TestStreamCreateSignsWithOwnedChannel(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	channel, channelID, _ := persistChannelUpdateFixture(t, &fixture)
	fixture.network.downloadComplete = nil
	fileMutationRPCResult(t, fixture.server, "stream_create", map[string]any{
		"name": "signed-video", "bid": "1.0", "sd_hash": strings.Repeat("33", 48),
		"file_name": "video.mp4", "channel_id": channelID,
	})
	fixture.network.mu.Lock()
	broadcasts := append([]string(nil), fixture.network.broadcasts...)
	fixture.network.mu.Unlock()
	if len(broadcasts) != 1 {
		t.Fatalf("stream broadcasts = %v", broadcasts)
	}
	transaction, err := walletpkg.ParseTransaction(mustDecodeHex(t, broadcasts[0]))
	value, decodeErr := walletpkg.DecodeClaimValue(transaction.Outputs[0].Script.Claim)
	channelValue, channelErr := walletpkg.DecodeClaimValue(channel.Outputs[0].Script.Claim)
	valid, verifyErr := walletpkg.VerifyTransactionClaimSignature(value, transaction, channelValue)
	if err != nil || decodeErr != nil || channelErr != nil || verifyErr != nil || !valid ||
		value.SigningChannelID() == nil || *value.SigningChannelID() != channelID {
		t.Fatalf("signed stream = %#v, valid %v, errors %v / %v / %v / %v", value, valid, err, decodeErr, channelErr, verifyErr)
	}
}

func persistStreamMutationFixture(t *testing.T, fixture *paidGetFixture, name string) (*walletpkg.Transaction, string) {
	t.Helper()
	addresses, err := fixture.account.Receiving.GetAddresses(context.Background(), false)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("stream addresses = %v, %v", addresses, err)
	}
	address := addresses[0]
	decoded, err := keys.DecodeBase58(address)
	if err != nil || len(decoded) < 21 {
		t.Fatalf("decode stream address = %x, %v", decoded, err)
	}
	claim, err := walletpkg.BuildStreamClaim(nil, false, map[string]any{
		"title": "Original", "file_name": name + ".mp4", "sd_hash": strings.Repeat("55", 48),
		"width": 640, "height": 360, "duration": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	transaction := walletpkg.NewTransaction().AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{7},
	}}).AddOutputs([]walletpkg.TransactionOutput{
		walletpkg.NewClaimNameOutput(200_000_000, name, claim, decoded[1:21]),
	})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	transaction.Height, transaction.Position, transaction.IsVerified = 5, 5, true
	claimID, err := transaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.ledger.Database.SaveTransactionIOBatch(context.Background(), []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: transaction.Raw, Height: 5, Position: 5, IsVerified: true,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: transaction.Outputs[0].ID(), Address: &address, Position: 0,
			Amount: 200_000_000, Script: transaction.Outputs[0].Script.Source,
			TXOType: walletpkg.TransactionOutputTypeStream, ClaimID: &claimID, ClaimName: &name, HasSource: true,
		}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}
	return transaction, claimID
}
