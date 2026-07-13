package wallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"lbry/daemon/wallet/ledgerdb"
)

var (
	ErrTransactionHistoryUnavailable = errors.New("wallet transaction history is unavailable")
	ErrTransactionOwnershipUnknown   = errors.New("wallet transaction ownership is unknown")
)

type TransactionHistoryOptions struct {
	Query                ledgerdb.TransactionQuery
	AnnotationAccountIDs []string
	ChannelKeyAccounts   []*Account
}

type TransactionHistoryItem struct {
	TXID          string                           `json:"txid"`
	Timestamp     *int64                           `json:"timestamp"`
	Date          *string                          `json:"date"`
	Confirmations int64                            `json:"confirmations"`
	Value         string                           `json:"value"`
	Fee           string                           `json:"fee"`
	ClaimInfo     []TransactionHistoryClaimInfo    `json:"claim_info"`
	UpdateInfo    []TransactionHistoryClaimInfo    `json:"update_info"`
	SupportInfo   []TransactionHistorySupportInfo  `json:"support_info"`
	AbandonInfo   []TransactionHistoryAbandonInfo  `json:"abandon_info"`
	PurchaseInfo  []TransactionHistoryPurchaseInfo `json:"purchase_info"`
}

type TransactionHistoryClaimInfo struct {
	Address      *string `json:"address"`
	BalanceDelta string  `json:"balance_delta"`
	Amount       string  `json:"amount"`
	ClaimID      string  `json:"claim_id"`
	ClaimName    string  `json:"claim_name"`
	NOut         uint32  `json:"nout"`
	IsSpent      *bool   `json:"is_spent"`
}

type TransactionHistorySupportInfo struct {
	Address      *string `json:"address"`
	BalanceDelta string  `json:"balance_delta"`
	Amount       string  `json:"amount"`
	ClaimID      string  `json:"claim_id"`
	ClaimName    string  `json:"claim_name"`
	IsTip        bool    `json:"is_tip"`
	NOut         uint32  `json:"nout"`
	IsSpent      *bool   `json:"is_spent"`
}

type TransactionHistoryAbandonInfo struct {
	Address      *string `json:"address"`
	BalanceDelta string  `json:"balance_delta"`
	Amount       string  `json:"amount"`
	ClaimID      string  `json:"claim_id"`
	ClaimName    string  `json:"claim_name"`
	NOut         uint32  `json:"nout"`
}

type TransactionHistoryPurchaseInfo struct {
	Address      *string `json:"address"`
	BalanceDelta string  `json:"balance_delta"`
	Amount       string  `json:"amount"`
	ClaimID      string  `json:"claim_id"`
	NOut         uint32  `json:"nout"`
	IsSpent      *bool   `json:"is_spent"`
}

// GetTransactionHistory projects hydrated wallet transactions into the
// compact legacy history representation used by transaction_list.
func (ledger *Ledger) GetTransactionHistory(
	ctx context.Context, options TransactionHistoryOptions,
) ([]TransactionHistoryItem, error) {
	transactions, err := ledger.GetTransactions(ctx, TransactionListOptions{
		Query:                options.Query,
		AnnotationAccountIDs: options.AnnotationAccountIDs,
		ChannelKeyAccounts:   options.ChannelKeyAccounts,
		IncludeIsSpent:       true,
		IncludeIsMyOutput:    true,
	})
	if err != nil {
		return nil, err
	}
	return ledger.summarizeTransactionHistory(transactions)
}

func (ledger *Ledger) GetTransactionHistoryCount(
	ctx context.Context, query ledgerdb.TransactionQuery,
) (int64, error) {
	return ledger.CountTransactions(ctx, query)
}

func (account *Account) GetTransactionHistory(
	ctx context.Context, query ledgerdb.TransactionQuery,
) ([]TransactionHistoryItem, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return nil, err
	}
	annotationAccountIDs, err := transactionWalletAccountIDs(account.wallet)
	if err != nil {
		return nil, err
	}
	query.AccountIDs = []string{accountID}
	var channelKeyAccounts []*Account
	if account.wallet != nil {
		channelKeyAccounts = append([]*Account(nil), account.wallet.Accounts...)
	}
	return ledger.GetTransactionHistory(ctx, TransactionHistoryOptions{
		Query: query, AnnotationAccountIDs: annotationAccountIDs,
		ChannelKeyAccounts: channelKeyAccounts,
	})
}

func (account *Account) GetTransactionHistoryCount(
	ctx context.Context, query ledgerdb.TransactionQuery,
) (int64, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return 0, err
	}
	query.AccountIDs = []string{accountID}
	return ledger.GetTransactionHistoryCount(ctx, query)
}

func (ledger *Ledger) summarizeTransactionHistory(
	transactions []*Transaction,
) ([]TransactionHistoryItem, error) {
	history := make([]TransactionHistoryItem, 0, len(transactions))
	if len(transactions) == 0 {
		return history, nil
	}
	if ledger == nil || ledger.Headers == nil {
		return nil, fmt.Errorf(
			"%w: ledger headers are unavailable", ErrTransactionHistoryUnavailable,
		)
	}
	tipHeight := int64(ledger.Headers.Height())
	for _, transaction := range transactions {
		if transaction == nil {
			return nil, fmt.Errorf(
				"%w: transaction is nil", ErrTransactionHistoryUnavailable,
			)
		}
		item, err := ledger.summarizeTransactionHistoryItem(transaction, tipHeight)
		if err != nil {
			return nil, err
		}
		history = append(history, item)
	}
	return history, nil
}

func (ledger *Ledger) summarizeTransactionHistoryItem(
	transaction *Transaction, tipHeight int64,
) (TransactionHistoryItem, error) {
	item := TransactionHistoryItem{
		TXID:         transaction.ID,
		ClaimInfo:    make([]TransactionHistoryClaimInfo, 0),
		UpdateInfo:   make([]TransactionHistoryClaimInfo, 0),
		SupportInfo:  make([]TransactionHistorySupportInfo, 0),
		AbandonInfo:  make([]TransactionHistoryAbandonInfo, 0),
		PurchaseInfo: make([]TransactionHistoryPurchaseInfo, 0),
	}
	if timestamp, ok := ledger.Headers.EstimatedTimestamp(int(transaction.Height), true); ok {
		item.Timestamp = &timestamp
		if transaction.Height > 0 {
			date := time.Unix(timestamp, 0).In(time.Local).Format("2006-01-02 15:04")
			item.Date = &date
		}
	}
	if transaction.Height > 0 {
		item.Confirmations = (tipHeight + 1) - transaction.Height
	}

	allInputsMine := transactionHistoryAllInputsMine(transaction)
	netBalance, fee, err := transactionHistoryNetBalanceAndFee(transaction)
	if err != nil {
		return TransactionHistoryItem{}, fmt.Errorf("summarize transaction %s: %w", transaction.ID, err)
	}
	if allInputsMine {
		item.Value = transactionHistoryDewies(new(big.Int).Add(netBalance, fee))
		item.Fee = transactionHistoryDewies(new(big.Int).Neg(fee))
	} else {
		item.Value = transactionHistoryDewies(netBalance)
		item.Fee = "0.0"
	}

	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		if transactionQueryBool(output.IsMyOutput) && output.Script.IsClaimName() {
			info, err := ledger.transactionHistoryClaimInfo(
				output, transactionHistorySignedAmount(output.Amount, true),
			)
			if err != nil {
				return TransactionHistoryItem{}, err
			}
			item.ClaimInfo = append(item.ClaimInfo, info)
		}
		if transactionQueryBool(output.IsMyOutput) && output.Script.IsUpdateClaim() {
			delta := new(big.Int)
			if allInputsMine {
				previous, err := transactionHistoryMatchingClaimInput(transaction, output)
				if err != nil {
					return TransactionHistoryItem{}, err
				}
				if previous == nil {
					continue
				}
				delta.Sub(
					new(big.Int).SetUint64(previous.Amount),
					new(big.Int).SetUint64(output.Amount),
				)
			}
			info, err := ledger.transactionHistoryClaimInfo(output, delta)
			if err != nil {
				return TransactionHistoryItem{}, err
			}
			item.UpdateInfo = append(item.UpdateInfo, info)
		}
		if transactionQueryBool(output.IsMyOutput) && output.Script.IsSupportClaim() {
			delta := transactionHistorySignedAmount(output.Amount, allInputsMine)
			info, err := ledger.transactionHistorySupportInfo(output, delta, !allInputsMine)
			if err != nil {
				return TransactionHistoryItem{}, err
			}
			item.SupportInfo = append(item.SupportInfo, info)
		}
	}
	if allInputsMine {
		for index := range transaction.Outputs {
			output := &transaction.Outputs[index]
			if transactionQueryBool(output.IsMyOutput) || !output.Script.IsSupportClaim() {
				continue
			}
			info, err := ledger.transactionHistorySupportInfo(
				output, transactionHistorySignedAmount(output.Amount, true), true,
			)
			if err != nil {
				return TransactionHistoryItem{}, err
			}
			item.SupportInfo = append(item.SupportInfo, info)
		}
	}
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		if input.ResolvedOutput == nil {
			continue
		}
		output := currentTransactionOutput(input.ResolvedOutput)
		if !transactionQueryBool(output.IsMyOutput) || !output.Script.IsClaimInvolved() {
			continue
		}
		if output.Script.IsClaimName() || output.Script.IsUpdateClaim() {
			updated, err := transactionHistoryHasOwnedUpdate(transaction, output)
			if err != nil {
				return TransactionHistoryItem{}, err
			}
			if updated {
				continue
			}
		}
		info, err := ledger.transactionHistoryAbandonInfo(output)
		if err != nil {
			return TransactionHistoryItem{}, err
		}
		item.AbandonInfo = append(item.AbandonInfo, info)
	}
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		if output.Purchase == nil {
			continue
		}
		claimID, ok := decodeTransactionPurchase(output.Purchase.Script)
		if !ok {
			return TransactionHistoryItem{}, fmt.Errorf(
				"%w: purchase data at output %d is invalid",
				ErrInvalidTransactionClaimID, output.Purchase.Position,
			)
		}
		address, err := ledger.transactionHistoryAddress(output)
		if err != nil {
			return TransactionHistoryItem{}, err
		}
		delta := transactionHistorySignedAmount(output.Amount, allInputsMine)
		item.PurchaseInfo = append(item.PurchaseInfo, TransactionHistoryPurchaseInfo{
			Address:      address,
			BalanceDelta: transactionHistoryDewies(delta),
			Amount:       transactionHistoryDewies(new(big.Int).SetUint64(output.Amount)),
			ClaimID:      claimID,
			NOut:         output.Position,
			IsSpent:      cloneTransactionQueryBool(output.IsSpent),
		})
	}
	return item, nil
}

func (ledger *Ledger) transactionHistoryClaimInfo(
	output *TransactionOutput, delta *big.Int,
) (TransactionHistoryClaimInfo, error) {
	address, err := ledger.transactionHistoryAddress(output)
	if err != nil {
		return TransactionHistoryClaimInfo{}, err
	}
	claimID, err := output.ClaimID()
	if err != nil {
		return TransactionHistoryClaimInfo{}, err
	}
	claimName, err := transactionHistoryClaimName(output)
	if err != nil {
		return TransactionHistoryClaimInfo{}, err
	}
	return TransactionHistoryClaimInfo{
		Address:      address,
		BalanceDelta: transactionHistoryDewies(delta),
		Amount:       transactionHistoryDewies(new(big.Int).SetUint64(output.Amount)),
		ClaimID:      claimID,
		ClaimName:    claimName,
		NOut:         output.Position,
		IsSpent:      cloneTransactionQueryBool(output.IsSpent),
	}, nil
}

func (ledger *Ledger) transactionHistorySupportInfo(
	output *TransactionOutput, delta *big.Int, isTip bool,
) (TransactionHistorySupportInfo, error) {
	address, err := ledger.transactionHistoryAddress(output)
	if err != nil {
		return TransactionHistorySupportInfo{}, err
	}
	claimID, err := output.ClaimID()
	if err != nil {
		return TransactionHistorySupportInfo{}, err
	}
	claimName, err := transactionHistoryClaimName(output)
	if err != nil {
		return TransactionHistorySupportInfo{}, err
	}
	return TransactionHistorySupportInfo{
		Address:      address,
		BalanceDelta: transactionHistoryDewies(delta),
		Amount:       transactionHistoryDewies(new(big.Int).SetUint64(output.Amount)),
		ClaimID:      claimID,
		ClaimName:    claimName,
		IsTip:        isTip,
		NOut:         output.Position,
		IsSpent:      cloneTransactionQueryBool(output.IsSpent),
	}, nil
}

func (ledger *Ledger) transactionHistoryAbandonInfo(
	output *TransactionOutput,
) (TransactionHistoryAbandonInfo, error) {
	address, err := ledger.transactionHistoryAddress(output)
	if err != nil {
		return TransactionHistoryAbandonInfo{}, err
	}
	claimID, err := output.ClaimID()
	if err != nil {
		return TransactionHistoryAbandonInfo{}, err
	}
	claimName, err := transactionHistoryClaimName(output)
	if err != nil {
		return TransactionHistoryAbandonInfo{}, err
	}
	amount := new(big.Int).SetUint64(output.Amount)
	return TransactionHistoryAbandonInfo{
		Address:      address,
		BalanceDelta: transactionHistoryDewies(amount),
		Amount:       transactionHistoryDewies(amount),
		ClaimID:      claimID,
		ClaimName:    claimName,
		NOut:         output.Position,
	}, nil
}

func (ledger *Ledger) transactionHistoryAddress(output *TransactionOutput) (*string, error) {
	address, err := output.Address(ledger.Network)
	if errors.Is(err, ErrTransactionHasNoAddress) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &address, nil
}

func transactionHistoryClaimName(output *TransactionOutput) (string, error) {
	if !utf8.Valid(output.Script.ClaimName) {
		return "", fmt.Errorf(
			"%w at output %d", ErrInvalidTransactionClaimName, output.Position,
		)
	}
	return string(output.Script.ClaimName), nil
}

func transactionHistoryAllInputsMine(transaction *Transaction) bool {
	for index := range transaction.Inputs {
		owned := transaction.Inputs[index].IsMyInput()
		if owned == nil || !*owned {
			return false
		}
	}
	return true
}

func transactionHistoryNetBalanceAndFee(
	transaction *Transaction,
) (*big.Int, *big.Int, error) {
	netBalance := new(big.Int)
	inputSum := new(big.Int)
	outputSum := new(big.Int)
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		if input.ResolvedOutput == nil {
			continue
		}
		output := currentTransactionOutput(input.ResolvedOutput)
		amount := new(big.Int).SetUint64(output.Amount)
		inputSum.Add(inputSum, amount)
		owned := input.IsMyInput()
		if owned == nil {
			return nil, nil, fmt.Errorf(
				"%w for input %d", ErrTransactionOwnershipUnknown, input.Position,
			)
		}
		if *owned {
			netBalance.Sub(netBalance, amount)
		}
	}
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		amount := new(big.Int).SetUint64(output.Amount)
		outputSum.Add(outputSum, amount)
		if output.IsMyOutput == nil {
			return nil, nil, fmt.Errorf(
				"%w for output %d", ErrTransactionOwnershipUnknown, output.Position,
			)
		}
		if *output.IsMyOutput {
			netBalance.Add(netBalance, amount)
		}
	}
	return netBalance, new(big.Int).Sub(inputSum, outputSum), nil
}

func transactionHistoryMatchingClaimInput(
	transaction *Transaction, update *TransactionOutput,
) (*TransactionOutput, error) {
	claimID, err := update.ClaimID()
	if err != nil {
		return nil, err
	}
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		if input.ResolvedOutput == nil {
			continue
		}
		output := currentTransactionOutput(input.ResolvedOutput)
		if !output.Script.IsClaimInvolved() {
			continue
		}
		otherClaimID, err := output.ClaimID()
		if err != nil {
			return nil, err
		}
		if otherClaimID == claimID {
			return output, nil
		}
	}
	return nil, nil
}

func transactionHistoryHasOwnedUpdate(
	transaction *Transaction, abandoned *TransactionOutput,
) (bool, error) {
	claimID, err := abandoned.ClaimID()
	if err != nil {
		return false, err
	}
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		if !transactionQueryBool(output.IsMyOutput) || !output.Script.IsUpdateClaim() {
			continue
		}
		updateClaimID, err := output.ClaimID()
		if err != nil {
			return false, err
		}
		if updateClaimID == claimID {
			return true, nil
		}
	}
	return false, nil
}

func transactionHistorySignedAmount(amount uint64, negative bool) *big.Int {
	value := new(big.Int).SetUint64(amount)
	if negative {
		value.Neg(value)
	}
	return value
}

func transactionHistoryDewies(value *big.Int) string {
	ratio := new(big.Rat).SetFrac(value, big.NewInt(100_000_000))
	decimal, _ := ratio.Float64()
	formatted := strings.TrimRight(strconv.FormatFloat(decimal, 'f', 8, 64), "0")
	if strings.HasSuffix(formatted, ".") {
		return formatted + "0"
	}
	return formatted
}
