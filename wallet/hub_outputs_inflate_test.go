package wallet

import (
	"errors"
	"testing"
)

func TestHubOutputsInflatePreservesRecursiveErrorAndMissingStates(t *testing.T) {
	outputs := &HubOutputs{
		TXOs: []*HubOutput{
			{Error: &HubError{
				Code: HubErrorBlocked,
				Text: "outer",
				Blocked: &HubBlocked{Channel: &HubOutput{Error: &HubError{
					Code: HubErrorInvalid, Text: "nested",
				}}},
			}},
			{Error: &HubError{Code: HubErrorBlocked, Text: "missing censor"}},
		},
		Blocked: []*HubBlocked{
			{Count: 4, Channel: &HubOutput{Error: &HubError{
				Code: HubErrorNotFound, Text: "blocked channel",
			}}},
			nil,
		},
		BlockedTotal: 5,
	}

	results, blocked, err := outputs.Inflate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Kind() != HubInflatedError ||
		results[0].Error.Censor == nil || results[0].Error.Censor.Error == nil ||
		results[0].Error.Censor.Error.Name != "INVALID" ||
		results[0].Error.Censor.Error.Text != "nested" {
		t.Fatalf("recursive blocked result = %#v", results)
	}
	if results[1].Error == nil || results[1].Error.Censor == nil ||
		!results[1].Error.Censor.IsMissing() {
		t.Fatalf("missing censor result = %#v", results[1])
	}
	if blocked.Total != 5 || len(blocked.Channels) != 2 ||
		blocked.Channels[0].Blocked != 4 ||
		blocked.Channels[0].Channel.Error == nil ||
		blocked.Channels[0].Channel.Error.Name != "NOT_FOUND" ||
		!blocked.Channels[1].Channel.IsMissing() {
		t.Fatalf("recursive blocked summary = %#v", blocked)
	}
}

func TestHubOutputsInflateClaimMetaAndStaleRelations(t *testing.T) {
	var hash [32]byte
	hash[0] = 1
	staleChannel := &TransactionOutput{Position: 30}
	staleRepost := &TransactionOutput{Position: 31}
	transaction := &Transaction{Hash: hash, Outputs: []TransactionOutput{
		{
			Position: 0, Meta: map[string]any{"stale": true},
			Channel: staleChannel, RepostedClaim: staleRepost,
		},
		{
			Position: 1,
			Script: TransactionOutputScript{
				Template: TransactionScriptClaimPubKeyHash,
				Claim:    []byte{0x00, 0x12, 0x00},
			},
		},
	}}
	outputs := &HubOutputs{TXOs: []*HubOutput{
		{
			TransactionHash: hash[:], Position: 0,
			Claim: &HubClaimMeta{
				ShortURL: "short#1", Reposted: 2, TrendingScore: 99.5,
			},
		},
		{
			TransactionHash: hash[:], Position: 1,
			Claim: &HubClaimMeta{ShortURL: "@channel#1"},
		},
	}}

	results, _, err := outputs.Inflate([]*Transaction{transaction})
	if err != nil {
		t.Fatal(err)
	}
	plain := results[0].Output
	if plain != &transaction.Outputs[0] || plain.Channel != staleChannel ||
		plain.RepostedClaim != staleRepost || plain.Meta["short_url"] != "lbry://short#1" ||
		plain.Meta["canonical_url"] != "lbry://short#1" || plain.Meta["reposted"] != uint32(2) {
		t.Fatalf("plain inflated output = %#v", plain)
	}
	if _, exists := plain.Meta["stale"]; exists {
		t.Fatalf("claim metadata did not replace stale map: %#v", plain.Meta)
	}
	if _, exists := plain.Meta["trending_score"]; exists {
		t.Fatalf("trending score leaked into legacy metadata: %#v", plain.Meta)
	}
	if _, exists := plain.Meta["claims_in_channel"]; exists {
		t.Fatalf("non-channel output has claims_in_channel: %#v", plain.Meta)
	}
	channel := results[1].Output
	count, exists := channel.Meta["claims_in_channel"]
	if !exists || count != uint32(0) {
		t.Fatalf("channel zero claims_in_channel = %#v", channel.Meta)
	}
}

func TestHubOutputsInflateRelationOrderAndPythonFailures(t *testing.T) {
	var rootHash, channelHash [32]byte
	rootHash[0], channelHash[0] = 1, 2
	root := &Transaction{Hash: rootHash, Outputs: []TransactionOutput{{Position: 0}}}
	channel := &Transaction{Hash: channelHash, Outputs: []TransactionOutput{{Position: 0}}}
	missingHash := []byte{0x80, '\'', '"', '\n', '\\'}
	outputs := &HubOutputs{TXOs: []*HubOutput{{
		TransactionHash: rootHash[:],
		Claim: &HubClaimMeta{
			ShortURL: "root#1",
			Channel:  &HubOutput{TransactionHash: channelHash[:]},
			Repost:   &HubOutput{TransactionHash: missingHash},
		},
	}}}

	_, _, err := outputs.Inflate([]*Transaction{root, channel})
	var inflateError *HubOutputsInflateError
	if !errors.As(err, &inflateError) || inflateError.PythonErrorName() != "KeyError" ||
		err.Error() != `b'\x80\'"\n\\'` {
		t.Fatalf("relation failure = %T %q", err, err)
	}
	if root.Outputs[0].Meta == nil || root.Outputs[0].Channel != &channel.Outputs[0] ||
		root.Outputs[0].RepostedClaim != nil {
		t.Fatalf("relation partial mutation = %#v", root.Outputs[0])
	}

	if got := pythonHubBytesRepr(nil); got != "b''" {
		t.Fatalf("empty bytes repr = %q", got)
	}
	if got := pythonHubBytesRepr([]byte{'\''}); got != `b"'"` {
		t.Fatalf("quoted bytes repr = %q", got)
	}
}

func TestHubInflatedResultKindsAndNilBoundaries(t *testing.T) {
	output := new(TransactionOutput)
	cases := []struct {
		result HubInflatedResult
		kind   HubInflatedResultKind
	}{
		{HubInflatedResult{Output: output}, HubInflatedOutput},
		{HubInflatedResult{Error: &HubResolveError{}}, HubInflatedError},
		{HubInflatedResult{}, HubInflatedMissing},
	}
	for _, test := range cases {
		if test.result.Kind() != test.kind ||
			test.result.IsMissing() != (test.kind == HubInflatedMissing) {
			t.Fatalf("result kind = %q missing %v, want %q",
				test.result.Kind(), test.result.IsMissing(), test.kind)
		}
	}

	var outputs *HubOutputs
	_, _, err := outputs.Inflate(nil)
	var inflateError *HubOutputsInflateError
	if !errors.Is(err, ErrHubOutputsInflation) || !errors.As(err, &inflateError) ||
		inflateError.PythonErrorName() != "AttributeError" {
		t.Fatalf("nil inflater error = %#v", err)
	}
}
