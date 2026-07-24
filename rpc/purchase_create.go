package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handlePurchaseCreate(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	result, err := rpcServer.purchaseCreate(normalized)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) purchaseCreate(normalized normalizedRPCParams) (map[string]any, error) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		return nil, errors.New("wallet manager is unavailable")
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		return nil, err
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		return nil, err
	}
	if selectedWallet == nil {
		return nil, errors.New("default wallet is unavailable")
	}
	if selectedWallet.IsLocked() {
		return nil, errors.New("Cannot spend funds with locked wallet, unlock first.")
	}
	fundingIDs, err := purchaseFundingAccountIDs(normalized.named["funding_account_ids"])
	if err != nil {
		return nil, err
	}
	accounts, err := selectedWallet.AccountsOrAll(fundingIDs)
	if err != nil {
		return nil, err
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	claimID := fileMutationOptionalString(normalized.named["claim_id"])
	url := fileMutationOptionalString(normalized.named["url"])
	var claim *walletpkg.TransactionOutput
	annotations := walletpkg.ResolvedTransactionOutputAnnotationOptions{
		Accounts: accounts, Wallet: selectedWallet, PurchaseReceiptRequested: true,
	}
	if claimID != nil && *claimID != "" {
		claim, err = ledger.GetClaimByClaimID(normalized.ctx, *claimID, annotations)
		if errors.Is(err, walletpkg.ErrClaimLookupMissing) {
			return nil, fmt.Errorf("Could not find claim with claim_id '%s'.", *claimID)
		}
		if err != nil {
			return nil, err
		}
	} else if url != nil && *url != "" {
		claim, err = purchaseResolveURL(normalized.ctx, ledger, *url, annotations)
		if errors.Is(err, walletpkg.ErrClaimLookupMissing) {
			return nil, fmt.Errorf("Could not find claim with url '%s'.", *url)
		}
		if err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("Missing argument claim_id or url.")
	}
	claimIDDisplay := "None"
	if claimID != nil {
		claimIDDisplay = *claimID
	}
	if !transactionListTruthy(normalized.named["allow_duplicate_purchase"]) && claim.PurchaseReceipt != nil {
		return nil, fmt.Errorf(
			"You already have a purchase for claim_id '%s'. Use", claimIDDisplay,
		)
	}
	_, claimValue, err := walletpkg.TransactionOutputStreamSource(claim)
	if err != nil || !managedClaimHasPrice(claimValue) {
		return nil, fmt.Errorf("Claim '%s' does not have a purchase price.", claimIDDisplay)
	}
	amount, address, err := purchaseClaimFee(
		claimValue, rpcServer.settings, rpcServer.exchangeRates,
		transactionListTruthy(normalized.named["override_max_key_fee"]),
	)
	if err != nil {
		return nil, err
	}
	transaction, err := walletpkg.CreatePurchaseTransaction(
		normalized.ctx, accounts, claim, amount, address,
	)
	if err != nil {
		return nil, err
	}
	if transactionListTruthy(normalized.named["preview"]) {
		if err := ledger.ReleaseTransaction(context.WithoutCancel(normalized.ctx), transaction); err != nil {
			return nil, err
		}
	} else if err := ledger.BroadcastOrRelease(
		normalized.ctx, transaction, transactionListTruthy(normalized.named["blocking"]),
	); err != nil {
		return nil, err
	}
	return ledger.LegacyTransactionJSON(transaction)
}

func purchaseResolveURL(
	ctx context.Context,
	ledger *walletpkg.Ledger,
	url string,
	annotations walletpkg.ResolvedTransactionOutputAnnotationOptions,
) (*walletpkg.TransactionOutput, error) {
	var output *walletpkg.TransactionOutput
	_, err := ledger.ResolveAndSnapshot(
		ctx, []walletpkg.ResolveRequest{{URL: url}}, annotations,
		walletpkg.LegacyTransactionJSONOptions{},
		func(outputs []*walletpkg.TransactionOutput) error {
			if len(outputs) != 1 || outputs[0] == nil {
				return walletpkg.ErrClaimLookupMissing
			}
			output = outputs[0]
			return nil
		},
	)
	return output, err
}

func purchaseFundingAccountIDs(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		result := make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("funding_account_ids item has type %T", item)
			}
			result[index] = text
		}
		return result, nil
	default:
		return nil, fmt.Errorf("funding_account_ids has type %T", value)
	}
}

func purchaseClaimFee(
	value *walletpkg.ClaimValue,
	settings SettingsStore,
	exchangeRates ExchangeRateConverter,
	overrideMaximum bool,
) (uint64, string, error) {
	if !overrideMaximum {
		return managedClaimPurchaseFee(value, settings, exchangeRates)
	}
	fee, ok := value.Value["fee"].(map[string]any)
	if !ok {
		return 0, "", errors.New("claim does not have a purchase price")
	}
	currency, _ := fee["currency"].(string)
	amountText, _ := fee["amount"].(string)
	amount, err := managedFeeToDewies(currency, amountText, exchangeRates)
	if err != nil {
		return 0, "", err
	}
	address, _ := fee["address"].(string)
	return amount, address, nil
}
