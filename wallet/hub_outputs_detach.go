package wallet

import (
	"context"
	"math/big"
)

// inflateDetachedHubOutputs keeps Hub inflation's shared-cache mutations under
// the ledger lock, then returns a graph with no Transaction or
// TransactionOutput aliases into that cache.
func (ledger *Ledger) inflateDetachedHubOutputs(
	ctx context.Context, outputs *HubOutputs,
) (HubOutputsPage, error) {
	transactions, err := ledger.prepareHubOutputs(ctx, outputs)
	if err != nil {
		return HubOutputsPage{}, err
	}

	ledger.hubOutputsInflateMu.Lock()
	defer ledger.hubOutputsInflateMu.Unlock()
	page, err := ledger.inflatePreparedHubOutputs(outputs, transactions)
	if err != nil {
		return HubOutputsPage{}, err
	}
	return detachHubOutputsPage(page), nil
}

func detachHubOutputsPage(page HubOutputsPage) HubOutputsPage {
	cloner := newTransactionOutputGraphCloner()
	detached := HubOutputsPage{
		Items: make([]HubInflatedResult, len(page.Items)),
		Blocked: HubBlockedSummary{
			Total:    page.Blocked.Total,
			Channels: make([]HubBlockedChannelSummary, len(page.Blocked.Channels)),
		},
		Offset: page.Offset,
		Total:  page.Total,
	}
	for index, result := range page.Items {
		detached.Items[index] = cloner.cloneHubResult(result)
	}
	for index, channel := range page.Blocked.Channels {
		detached.Blocked.Channels[index] = HubBlockedChannelSummary{
			Channel: cloner.cloneHubResult(channel.Channel),
			Blocked: channel.Blocked,
		}
	}
	return detached
}

type transactionOutputGraphCloner struct {
	transactions map[*Transaction]*Transaction
	outputs      map[*TransactionOutput]*TransactionOutput
}

func newTransactionOutputGraphCloner() *transactionOutputGraphCloner {
	return &transactionOutputGraphCloner{
		transactions: make(map[*Transaction]*Transaction),
		outputs:      make(map[*TransactionOutput]*TransactionOutput),
	}
}

func (cloner *transactionOutputGraphCloner) cloneHubResult(
	result HubInflatedResult,
) HubInflatedResult {
	cloned := HubInflatedResult{Output: cloner.cloneOutput(result.Output)}
	if result.Error != nil {
		cloned.Error = &HubResolveError{Name: result.Error.Name, Text: result.Error.Text}
		if result.Error.Censor != nil {
			censor := cloner.cloneHubResult(*result.Error.Censor)
			cloned.Error.Censor = &censor
		}
	}
	return cloned
}

func (cloner *transactionOutputGraphCloner) cloneOutput(
	output *TransactionOutput,
) *TransactionOutput {
	output = currentTransactionOutput(output)
	if output == nil {
		return nil
	}
	if cloned, exists := cloner.outputs[output]; exists {
		return cloned
	}
	if output.owner != nil && uint64(output.Position) < uint64(len(output.owner.Outputs)) {
		transaction := cloner.cloneTransaction(output.owner)
		return &transaction.Outputs[output.Position]
	}

	cloned := *output
	cloned.owner = nil
	cloner.outputs[output] = &cloned
	cloner.cloneOutputFields(output, &cloned)
	return &cloned
}

func (cloner *transactionOutputGraphCloner) cloneTransaction(
	transaction *Transaction,
) *Transaction {
	if transaction == nil {
		return nil
	}
	if cloned, exists := cloner.transactions[transaction]; exists {
		return cloned
	}

	cloned := *transaction
	cloned.Raw = append([]byte(nil), transaction.Raw...)
	cloned.RawSansSegWit = append([]byte(nil), transaction.RawSansSegWit...)
	cloned.Trailing = append([]byte(nil), transaction.Trailing...)
	cloned.Witnesses = cloneTransactionScriptByteSlices(transaction.Witnesses)
	if transaction.JulianDay != nil {
		value := *transaction.JulianDay
		cloned.JulianDay = &value
	}
	cloned.Inputs = make([]TransactionInput, len(transaction.Inputs))
	cloned.Outputs = make([]TransactionOutput, len(transaction.Outputs))
	cloner.transactions[transaction] = &cloned

	for index := range transaction.Inputs {
		cloned.Inputs[index] = transaction.Inputs[index]
		cloned.Inputs[index].owner = &cloned
	}
	for index := range transaction.Outputs {
		cloned.Outputs[index] = transaction.Outputs[index]
		cloned.Outputs[index].owner = &cloned
		cloner.outputs[&transaction.Outputs[index]] = &cloned.Outputs[index]
	}
	for index := range transaction.Inputs {
		source := &transaction.Inputs[index]
		target := &cloned.Inputs[index]
		target.Script = cloneTransactionInputScript(source.Script)
		target.Coinbase = append([]byte(nil), source.Coinbase...)
		target.ResolvedOutput = cloner.cloneOutput(source.ResolvedOutput)
	}
	for index := range transaction.Outputs {
		cloner.cloneOutputFields(&transaction.Outputs[index], &cloned.Outputs[index])
	}
	return &cloned
}

func (cloner *transactionOutputGraphCloner) cloneOutputFields(
	source, target *TransactionOutput,
) {
	target.Script = cloneTransactionOutputScript(source.Script)
	target.IsInternalTransfer = cloneTransactionQueryBool(source.IsInternalTransfer)
	target.IsSpent = cloneTransactionQueryBool(source.IsSpent)
	target.IsMyOutput = cloneTransactionQueryBool(source.IsMyOutput)
	target.IsMyInput = cloneTransactionQueryBool(source.IsMyInput)
	target.SentSupports = cloneTransactionQueryInt64(source.SentSupports)
	target.SentTips = cloneTransactionQueryInt64(source.SentTips)
	target.ReceivedTips = cloneTransactionQueryInt64(source.ReceivedTips)
	target.transactionHeight = cloneTransactionQueryInt64(source.transactionHeight)
	target.Meta = cloneTransactionOutputMeta(source.Meta)
	target.Purchase = cloner.cloneOutput(source.Purchase)
	target.PurchasedClaim = cloner.cloneOutput(source.PurchasedClaim)
	target.PurchaseReceipt = cloner.cloneOutput(source.PurchaseReceipt)
	target.RepostedClaim = cloner.cloneOutput(source.RepostedClaim)
	target.Channel = cloner.cloneOutput(source.Channel)
	if source.Claims == nil {
		target.Claims = nil
	} else {
		target.Claims = make([]*TransactionOutput, len(source.Claims))
		for index, claim := range source.Claims {
			target.Claims[index] = cloner.cloneOutput(claim)
		}
	}
}

func cloneTransactionOutputScript(script TransactionOutputScript) TransactionOutputScript {
	cloned := script
	cloned.Source = append([]byte(nil), script.Source...)
	cloned.PublicKey = append([]byte(nil), script.PublicKey...)
	cloned.PubKeyHash = append([]byte(nil), script.PubKeyHash...)
	cloned.ScriptHash = append([]byte(nil), script.ScriptHash...)
	cloned.Data = append([]byte(nil), script.Data...)
	cloned.ClaimName = append([]byte(nil), script.ClaimName...)
	cloned.ClaimID = append([]byte(nil), script.ClaimID...)
	cloned.Claim = append([]byte(nil), script.Claim...)
	cloned.Support = append([]byte(nil), script.Support...)
	return cloned
}

func cloneTransactionInputScript(script TransactionInputScript) TransactionInputScript {
	cloned := script
	cloned.Source = append([]byte(nil), script.Source...)
	cloned.Signature = append([]byte(nil), script.Signature...)
	cloned.PublicKey = append([]byte(nil), script.PublicKey...)
	cloned.Signatures = cloneTransactionScriptByteSlices(script.Signatures)
	if script.Script != nil {
		subscript := *script.Script
		subscript.Source = append([]byte(nil), script.Script.Source...)
		subscript.PubKeyHash = append([]byte(nil), script.Script.PubKeyHash...)
		subscript.PublicKeys = cloneTransactionScriptByteSlices(script.Script.PublicKeys)
		if script.Script.Height != nil {
			subscript.Height = new(big.Int).Set(script.Script.Height)
		}
		cloned.Script = &subscript
	}
	return cloned
}

func cloneTransactionOutputMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	cloned := make(map[string]any, len(meta))
	for key, value := range meta {
		cloned[key] = cloneTransactionOutputMetaValue(value)
	}
	return cloned
}

func cloneTransactionOutputMetaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneTransactionOutputMeta(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneTransactionOutputMetaValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	case *big.Int:
		if typed == nil {
			return (*big.Int)(nil)
		}
		return new(big.Int).Set(typed)
	default:
		return value
	}
}
