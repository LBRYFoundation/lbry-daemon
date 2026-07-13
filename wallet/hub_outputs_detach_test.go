package wallet

import (
	"math/big"
	"testing"
)

func TestDetachHubOutputsPageDeepClonesTransactionGraph(t *testing.T) {
	height := big.NewInt(7)
	transaction := NewTransaction()
	transaction.Raw = []byte{1, 2, 3}
	transaction.Witnesses = [][]byte{{4, 5}}
	transaction.Inputs = []TransactionInput{{
		Script: TransactionInputScript{
			Source: []byte{6},
			Script: &TransactionInputSubscript{
				Source: []byte{7}, Height: height, PublicKeys: [][]byte{{8}},
			},
		},
	}}
	transaction.Outputs = []TransactionOutput{
		{
			Position: 0,
			Script: TransactionOutputScript{
				Source: []byte{9}, Claim: []byte{10}, ClaimName: []byte("root"),
			},
			Meta: map[string]any{
				"nested": map[string]any{"bytes": []byte{11}},
			},
		},
		{Position: 1, Script: TransactionOutputScript{Source: []byte{12}}},
	}
	for index := range transaction.Inputs {
		transaction.Inputs[index].owner = transaction
	}
	for index := range transaction.Outputs {
		transaction.Outputs[index].owner = transaction
	}
	root := &transaction.Outputs[0]
	related := &transaction.Outputs[1]
	root.Channel = related
	root.RepostedClaim = related
	root.Claims = []*TransactionOutput{related, nil, related}
	transaction.Inputs[0].ResolvedOutput = related

	page := detachHubOutputsPage(HubOutputsPage{
		Items: []HubInflatedResult{{Output: root}},
		Blocked: HubBlockedSummary{Channels: []HubBlockedChannelSummary{{
			Channel: HubInflatedResult{Output: related}, Blocked: 2,
		}}},
	})
	detached := page.Items[0].Output
	if detached == nil || detached == root || detached.owner == transaction ||
		detached.Channel == related || detached.Channel != detached.RepostedClaim ||
		detached.Claims[0] != detached.Channel || detached.Claims[2] != detached.Channel ||
		page.Blocked.Channels[0].Channel.Output != detached.Channel {
		t.Fatalf("detached graph aliases = root %#v, related %#v", detached, detached.Channel)
	}
	if detached.owner.Inputs[0].ResolvedOutput != detached.Channel {
		t.Fatalf("detached input relation = %#v", detached.owner.Inputs[0].ResolvedOutput)
	}

	detached.owner.Raw[0] = 21
	detached.owner.Witnesses[0][0] = 22
	detached.Script.Source[0] = 23
	detached.owner.Inputs[0].Script.Script.Height.SetInt64(24)
	detached.Meta["nested"].(map[string]any)["bytes"].([]byte)[0] = 25
	detached.Channel.Script.Source[0] = 26
	if transaction.Raw[0] != 1 || transaction.Witnesses[0][0] != 4 ||
		root.Script.Source[0] != 9 || height.Int64() != 7 ||
		root.Meta["nested"].(map[string]any)["bytes"].([]byte)[0] != 11 ||
		related.Script.Source[0] != 12 {
		t.Fatalf("detached graph mutation leaked into source: %#v", transaction)
	}
}
