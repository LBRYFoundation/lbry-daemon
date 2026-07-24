package wallet

import (
	"context"
	"math/big"

	"lbry/daemon/wallet/ledgerdb"
)

// ResolvedTransactionOutputAnnotationOptions is the wallet-owned annotation
// subset of Ledger._inflate_outputs.
type ResolvedTransactionOutputAnnotationOptions struct {
	AccountIDs               []string
	Accounts                 []*Account
	Wallet                   *Wallet
	PurchaseReceiptRequested bool
	IncludeIsMyOutput        bool
	IncludeSentSupports      bool
	IncludeSentTips          bool
	IncludeReceivedTips      bool
}

// EnrichResolvedTransactionOutputAnnotations makes throwaway output copies and
// applies wallet-specific annotations without leaking them into cached outputs.
func (ledger *Ledger) EnrichResolvedTransactionOutputAnnotations(
	ctx context.Context,
	outputs []*TransactionOutput,
	options ResolvedTransactionOutputAnnotationOptions,
) ([]*TransactionOutput, error) {
	enriched := make([]*TransactionOutput, len(outputs))
	for index, output := range outputs {
		if output == nil {
			continue
		}
		enriched[index] = cloneResolvedTransactionOutputForAnnotations(output)
	}

	// This deliberately omits IncludeReceivedTips, matching the pinned tuple.
	queryAnnotations := options.PurchaseReceiptRequested || options.IncludeIsMyOutput ||
		options.IncludeSentSupports || options.IncludeSentTips
	if !queryAnnotations {
		return enriched, nil
	}
	if len(options.AccountIDs) == 0 && len(options.Accounts) == 0 {
		return enriched, nil
	}
	pricedClaims := make([]*TransactionOutput, 0, len(enriched))
	hasDecodableClaim := false
	for _, output := range enriched {
		if transactionOutputCanDecodeClaim(output) {
			hasDecodableClaim = true
		}
		if options.PurchaseReceiptRequested && transactionOutputHasPrice(output) {
			pricedClaims = append(pricedClaims, output)
		}
	}
	needsClaimQueries := hasDecodableClaim && (options.IncludeIsMyOutput ||
		options.IncludeSentSupports || options.IncludeSentTips || options.IncludeReceivedTips)
	if len(pricedClaims) == 0 && !needsClaimQueries {
		return enriched, nil
	}
	accountIDs := append([]string(nil), options.AccountIDs...)
	if len(accountIDs) == 0 {
		var err error
		accountIDs, err = transactionPurchaseAccountIDs(options.Accounts)
		if err != nil {
			return nil, err
		}
	}
	if err := validateTransactionOutputQuery(ledger); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	receipts := make(map[string]*TransactionOutput)
	if options.PurchaseReceiptRequested {
		pricedClaimIDs := make([]string, 0, len(pricedClaims))
		for _, output := range pricedClaims {
			claimID, err := output.ClaimID()
			if err != nil {
				return nil, err
			}
			pricedClaimIDs = append(pricedClaimIDs, claimID)
		}
		if len(pricedClaimIDs) > 0 {
			var channelKeyAccounts []*Account
			if options.Wallet != nil {
				channelKeyAccounts = options.Wallet.Accounts
			}
			purchases, err := ledger.getPurchasesByAccountIDs(
				ctx,
				ledgerdb.TransactionQuery{PurchasedClaimIDs: pricedClaimIDs},
				accountIDs,
				channelKeyAccounts,
			)
			if err != nil {
				return nil, err
			}
			for _, purchase := range purchases {
				if purchase == nil || purchase.Purchase == nil {
					continue
				}
				claimID, ok := decodeTransactionPurchase(purchase.Purchase.Script)
				if ok {
					receipts[claimID] = purchase
				}
			}
		}
	}

	for _, output := range enriched {
		if !transactionOutputCanDecodeClaim(output) {
			continue
		}
		claimID, err := output.ClaimID()
		if err != nil {
			return nil, err
		}
		if options.PurchaseReceiptRequested {
			output.PurchaseReceipt = receipts[claimID]
		}
		if options.IncludeIsMyOutput {
			mine, err := ledger.Database.CountOutputs(ctx, ledgerdb.OutputQuery{
				AccountIDs: accountIDs,
				ClaimIDs:   []string{claimID},
				Types: []int64{
					TransactionOutputTypeStream,
					TransactionOutputTypeChannel,
					TransactionOutputTypeCollection,
					TransactionOutputTypeRepost,
				},
				IsMyOutput: transactionOutputEnrichmentBool(true),
				IsSpent:    transactionOutputEnrichmentBool(false),
			})
			if err != nil {
				return nil, err
			}
			output.IsMyOutput = transactionQueryBoolPointer(mine != 0)
		}
		if options.IncludeSentSupports {
			amount, err := ledger.sumResolvedTransactionOutputSupports(
				ctx, accountIDs, claimID, true, true,
				transactionOutputEnrichmentBool(false),
			)
			if err != nil {
				return nil, err
			}
			output.SentSupports = cloneTransactionQueryInt64(&amount)
		}
		if options.IncludeSentTips {
			amount, err := ledger.sumResolvedTransactionOutputSupports(
				ctx, accountIDs, claimID, true, false, nil,
			)
			if err != nil {
				return nil, err
			}
			output.SentTips = cloneTransactionQueryInt64(&amount)
		}
		if options.IncludeReceivedTips {
			amount, err := ledger.sumResolvedTransactionOutputSupports(
				ctx, accountIDs, claimID, false, true, nil,
			)
			if err != nil {
				return nil, err
			}
			output.ReceivedTips = cloneTransactionQueryInt64(&amount)
		}
	}
	return enriched, nil
}

func transactionOutputHasPrice(output *TransactionOutput) bool {
	if !transactionOutputCanDecodeClaim(output) {
		return false
	}
	decoded, err := decodeTransactionWireClaimValue(output.Script.Claim)
	if err != nil || decoded.value == nil || decoded.value.Type != "stream" {
		return false
	}
	fee, ok := decoded.value.Value["fee"].(map[string]any)
	if !ok {
		return false
	}
	amount, ok := fee["amount"].(string)
	if !ok {
		return false
	}
	parsed, ok := new(big.Rat).SetString(amount)
	return ok && parsed.Sign() > 0
}

func (ledger *Ledger) sumResolvedTransactionOutputSupports(
	ctx context.Context,
	accountIDs []string,
	claimID string,
	isMyInput bool,
	isMyOutput bool,
	isSpent *bool,
) (int64, error) {
	return ledger.Database.SumOutputs(ctx, ledgerdb.OutputQuery{
		AccountIDs: accountIDs,
		ClaimIDs:   []string{claimID},
		Types:      []int64{TransactionOutputTypeSupport},
		IsMyInput:  transactionOutputEnrichmentBool(isMyInput),
		IsMyOutput: transactionOutputEnrichmentBool(isMyOutput),
		IsSpent:    isSpent,
	})
}

func transactionOutputCanDecodeClaim(output *TransactionOutput) bool {
	if output == nil || (!output.Script.IsClaimName() && !output.Script.IsUpdateClaim()) {
		return false
	}
	_, err := decodeTransactionWireClaimValue(output.Script.Claim)
	return err == nil
}

func cloneResolvedTransactionOutputForAnnotations(output *TransactionOutput) *TransactionOutput {
	output = currentTransactionOutput(output)
	if output == nil {
		return nil
	}
	var cloned *TransactionOutput
	if output.owner != nil && uint64(output.Position) < uint64(len(output.owner.Outputs)) {
		owner := *output.owner
		owner.Inputs = append([]TransactionInput(nil), output.owner.Inputs...)
		owner.Outputs = append([]TransactionOutput(nil), output.owner.Outputs...)
		for index := range owner.Inputs {
			owner.Inputs[index].owner = &owner
		}
		for index := range owner.Outputs {
			owner.Outputs[index].owner = &owner
		}
		cloned = &owner.Outputs[output.Position]
	} else {
		value := *output
		cloned = &value
	}
	channel := cloned.Channel
	cloned.IsInternalTransfer = nil
	cloned.IsSpent = nil
	cloned.IsMyOutput = nil
	cloned.IsMyInput = nil
	cloned.SentSupports = nil
	cloned.SentTips = nil
	cloned.ReceivedTips = nil
	cloned.Channel = channel
	cloned.PrivateKey = nil
	cloned.PurchaseReceipt = nil
	return cloned
}

func transactionOutputEnrichmentBool(value bool) *bool {
	return &value
}
