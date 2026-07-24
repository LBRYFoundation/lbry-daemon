package rpc

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

type detailedBalance struct {
	Total, Reserved, Claims, Supports, MySupports int64
}

func (rpcServer *RPCServer) handleAddressIsMine(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		panic(err)
	}
	account, err := wallet.AccountOrDefault(accountID)
	if err != nil || account == nil {
		panic(err)
	}
	address, _ := normalized.named["address"].(string)
	limit := 1
	ledger := rpcServer.walletManagerProvider().DefaultLedger()
	records, err := ledger.Database.GetAddresses(normalized.ctx, ledgerdb.AddressQuery{
		Account: account.ID, Address: &address, Limit: &limit,
	})
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, len(records) != 0)
}

func (rpcServer *RPCServer) handleAddressUnused(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		panic(err)
	}
	account, err := wallet.AccountOrDefault(accountID)
	if err != nil || account == nil || account.Receiving == nil {
		panic(errors.New("receiving address manager is unavailable"))
	}
	address, err := account.Receiving.GetOrCreateUsableAddress(normalized.ctx)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, address)
}

func (rpcServer *RPCServer) handleAddressList(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accounts := wallet.Accounts
	if accountID, err := transactionListAccountID(normalized.named["account_id"]); err != nil {
		panic(err)
	} else if accountID != nil {
		account, err := wallet.AccountOrDefault(accountID)
		if err != nil {
			panic(err)
		}
		accounts = []*walletpkg.Account{account}
	}
	var addressFilter *string
	if address, ok := normalized.named["address"].(string); ok && address != "" {
		addressFilter = &address
	}
	items := make([]any, 0)
	for _, account := range accounts {
		ledger := rpcServer.walletManagerProvider().DefaultLedger()
		records, err := ledger.Database.GetAddresses(normalized.ctx, ledgerdb.AddressQuery{
			Account: account.ID, Address: addressFilter,
		})
		if err != nil {
			panic(err)
		}
		for _, record := range records {
			publicKey, err := keys.NewPublicKey(
				account.Network, record.PublicKey, record.ChainCode, record.N, int(record.Depth), nil,
			)
			if err != nil {
				panic(err)
			}
			items = append(items, map[string]any{
				"address": record.Address, "account": record.Account,
				"used_times": record.UsedTimes, "pubkey": publicKey.ExtendedKeyString(),
			})
		}
	}
	page := walletListPositiveInteger(normalized.named["page"], 1)
	pageSize := walletListPositiveInteger(normalized.named["page_size"], 20)
	total := len(items)
	start := pageSize * (page - 1)
	end := min(start+pageSize, total)
	paged := make([]any, 0)
	if start <= total {
		paged = append(paged, items[start:end]...)
	}
	sendResultResponse(response, map[string]any{
		"items": paged, "total_pages": (total + pageSize - 1) / pageSize,
		"total_items": total, "page": page, "page_size": pageSize,
	})
}

func (rpcServer *RPCServer) handleAccountBalance(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		panic(err)
	}
	account, err := wallet.AccountOrDefault(accountID)
	if err != nil || account == nil {
		panic(err)
	}
	confirmations := balanceConfirmations(normalized.named["confirmations"])
	balance, err := calculateDetailedBalance(
		normalized.ctx, rpcServer.walletManagerProvider().DefaultLedger(), []*walletpkg.Account{account}, confirmations,
	)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, encodeDetailedBalance(balance))
}

func (rpcServer *RPCServer) handleWalletBalance(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	manager := rpcServer.walletManagerProvider()
	ledger := manager.DefaultLedger()
	balance, err := calculateDetailedBalance(
		normalized.ctx, ledger, wallet.Accounts, balanceConfirmations(normalized.named["confirmations"]),
	)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, encodeDetailedBalance(balance))
}

func calculateDetailedBalance(
	ctx context.Context, ledger *walletpkg.Ledger, accounts []*walletpkg.Account, confirmations int64,
) (detailedBalance, error) {
	var result detailedBalance
	if ledger == nil || ledger.Database == nil {
		return result, errors.New("wallet ledger database is unavailable")
	}
	accountIDs := make([]string, len(accounts))
	for index, account := range accounts {
		accountIDs[index] = account.ID
	}
	unspent := false
	query := ledgerdb.OutputQuery{
		AccountIDs: accountIDs, AnnotationAccountIDs: accountIDs, IsSpent: &unspent,
		IncludeIsMyInput: true, NoTransaction: true,
	}
	if confirmations > 0 {
		if ledger.Headers == nil {
			return result, errors.New("ledger headers are unavailable")
		}
		maximum := int64(ledger.Headers.Height()) - (confirmations - 1)
		zero := int64(0)
		query.HeightLTE, query.HeightGT = &maximum, &zero
	}
	rows, err := ledger.Database.ListOutputs(ctx, query)
	if err != nil {
		return result, err
	}
	for _, row := range rows {
		result.Total += row.Amount
		if row.TXOType != walletpkg.TransactionOutputTypeOther && row.TXOType != walletpkg.TransactionOutputTypePurchase {
			result.Reserved += row.Amount
		}
		switch row.TXOType {
		case walletpkg.TransactionOutputTypeStream, walletpkg.TransactionOutputTypeChannel,
			walletpkg.TransactionOutputTypeCollection, walletpkg.TransactionOutputTypeRepost:
			result.Claims += row.Amount
		case walletpkg.TransactionOutputTypeSupport:
			result.Supports += row.Amount
			if row.IsMyInput != nil && *row.IsMyInput {
				result.MySupports += row.Amount
			}
		}
	}
	return result, nil
}

func encodeDetailedBalance(balance detailedBalance) map[string]any {
	return map[string]any{
		"total": dewiesString(balance.Total), "available": dewiesString(balance.Total - balance.Reserved),
		"reserved": dewiesString(balance.Reserved),
		"reserved_subtotals": map[string]any{
			"claims": dewiesString(balance.Claims), "supports": dewiesString(balance.MySupports),
			"tips": dewiesString(balance.Supports - balance.MySupports),
		},
	}
}

func dewiesString(value int64) string {
	ratio := new(big.Rat).SetFrac(big.NewInt(value), big.NewInt(100_000_000))
	decimal, _ := ratio.Float64()
	formatted := strings.TrimRight(strconv.FormatFloat(decimal, 'f', 8, 64), "0")
	if strings.HasSuffix(formatted, ".") {
		return formatted + "0"
	}
	return formatted
}

func balanceConfirmations(value any) int64 {
	if value == nil {
		return 0
	}
	parsed, _ := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	return parsed
}
