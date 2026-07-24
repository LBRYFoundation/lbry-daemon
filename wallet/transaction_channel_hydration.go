package wallet

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"lbry/daemon/wallet/ledgerdb"
)

var ErrTransactionChannelHydrationCycle = errors.New("transaction signing channel cycle")

type transactionChannelHydrationState struct {
	ledger             *Ledger
	ctx                context.Context
	channelKeyAccounts []*Account
	loaded             map[string]*TransactionOutput
	queried            map[string]bool
	resolved           map[string]bool
	active             map[string]bool
}

func newTransactionChannelHydrationState(
	ledger *Ledger, ctx context.Context, channelKeyAccounts ...[]*Account,
) *transactionChannelHydrationState {
	var accounts []*Account
	if len(channelKeyAccounts) > 0 {
		accounts = append([]*Account(nil), channelKeyAccounts[0]...)
	}
	return &transactionChannelHydrationState{
		ledger: ledger, ctx: ctx, channelKeyAccounts: accounts,
		loaded: make(map[string]*TransactionOutput), queried: make(map[string]bool),
		resolved: make(map[string]bool), active: make(map[string]bool),
	}
}

func (state *transactionChannelHydrationState) Hydrate(
	outputs map[string]*TransactionOutput,
) error {
	if state == nil || len(outputs) == 0 {
		return nil
	}
	outputIDs := make([]string, 0, len(outputs))
	channelIDs := make([]string, 0, len(outputs))
	for outputID := range outputs {
		outputIDs = append(outputIDs, outputID)
	}
	sort.Strings(outputIDs)
	return state.hydrateOrdered(outputs, outputIDs, channelIDs)
}

func (state *transactionChannelHydrationState) HydrateRows(
	rows []ledgerdb.OutputRow, outputs map[string]*TransactionOutput,
) error {
	if state == nil || len(outputs) == 0 {
		return nil
	}
	outputIDs := make([]string, 0, len(outputs))
	seen := make(map[string]struct{}, len(outputs))
	for _, row := range rows {
		if outputs[row.TXOID] == nil {
			continue
		}
		if _, exists := seen[row.TXOID]; exists {
			continue
		}
		seen[row.TXOID] = struct{}{}
		outputIDs = append(outputIDs, row.TXOID)
	}
	remaining := make([]string, 0, len(outputs)-len(outputIDs))
	for outputID := range outputs {
		if _, exists := seen[outputID]; !exists {
			remaining = append(remaining, outputID)
		}
	}
	sort.Strings(remaining)
	outputIDs = append(outputIDs, remaining...)
	return state.hydrateOrdered(outputs, outputIDs, nil)
}

func (state *transactionChannelHydrationState) hydrateOrdered(
	outputs map[string]*TransactionOutput, outputIDs, channelIDs []string,
) error {
	for _, outputID := range outputIDs {
		output := outputs[outputID]
		if err := state.hydrateChannelPrivateKey(output); err != nil {
			return err
		}
		if channelID, ok := transactionOutputSigningChannelID(output); ok {
			channelIDs = append(channelIDs, channelID)
		}
	}
	if err := state.ensureLoaded(channelIDs); err != nil {
		return err
	}
	for _, outputID := range outputIDs {
		output := outputs[outputID]
		channelID, ok := transactionOutputSigningChannelID(output)
		if !ok {
			continue
		}
		channel, err := state.resolve(channelID)
		if err != nil {
			return err
		}
		output.Channel = channel
	}
	return nil
}

func (state *transactionChannelHydrationState) hydrateChannelPrivateKey(
	output *TransactionOutput,
) error {
	if len(state.channelKeyAccounts) == 0 || output == nil {
		return nil
	}
	output = currentTransactionOutput(output)
	publicKey, isChannel, err := DecodeChannelClaimPublicKey(
		output.Script.Source,
	)
	if errors.Is(err, ErrDecodedClaimNotChannel) || !isChannel {
		return nil
	}
	if err != nil {
		return err
	}
	for _, account := range state.channelKeyAccounts {
		privateKey, err := account.GetChannelPrivateKey(publicKey, nil)
		if err != nil {
			return err
		}
		if privateKey != nil {
			output.PrivateKey = privateKey
			break
		}
	}
	return nil
}

func (state *transactionChannelHydrationState) ensureLoaded(channelIDs []string) error {
	unique := make(map[string]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID == "" || state.queried[channelID] {
			continue
		}
		if _, exists := state.loaded[channelID]; exists {
			continue
		}
		unique[channelID] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for channelID := range unique {
		ordered = append(ordered, channelID)
	}
	sort.Strings(ordered)
	unspent := false
	for offset := 0; offset < len(ordered); offset += TransactionQueryBatchSize {
		end := min(offset+TransactionQueryBatchSize, len(ordered))
		batch := append([]string(nil), ordered[offset:end]...)
		rows, err := state.ledger.Database.ListOutputs(state.ctx, ledgerdb.OutputQuery{
			ClaimIDs: batch,
			Types:    []int64{TransactionOutputTypeChannel},
			IsSpent:  &unspent,
		})
		if err != nil {
			return err
		}
		for _, channelID := range batch {
			state.queried[channelID] = true
		}
		hydrated, err := hydrateTransactionQueryOutputRows(rows, TransactionListOptions{})
		if err != nil {
			return err
		}
		if err := state.hydrateChannelPrivateKeys(rows, hydrated); err != nil {
			return err
		}
		for _, row := range rows {
			output := hydrated[row.TXOID]
			if output == nil {
				continue
			}
			claimID, err := output.ClaimID()
			if err != nil {
				return err
			}
			state.loaded[claimID] = output
		}
	}
	return nil
}

func (state *transactionChannelHydrationState) hydrateChannelPrivateKeys(
	rows []ledgerdb.OutputRow, outputs map[string]*TransactionOutput,
) error {
	seen := make(map[string]struct{}, len(outputs))
	for _, row := range rows {
		if _, exists := seen[row.TXOID]; exists {
			continue
		}
		seen[row.TXOID] = struct{}{}
		if err := state.hydrateChannelPrivateKey(outputs[row.TXOID]); err != nil {
			return err
		}
	}
	return nil
}

func (state *transactionChannelHydrationState) resolve(
	channelID string,
) (*TransactionOutput, error) {
	if state.resolved[channelID] {
		return state.loaded[channelID], nil
	}
	if state.active[channelID] {
		return nil, fmt.Errorf("%w: %s", ErrTransactionChannelHydrationCycle, channelID)
	}
	if err := state.ensureLoaded([]string{channelID}); err != nil {
		return nil, err
	}
	channel := state.loaded[channelID]
	if channel == nil {
		state.resolved[channelID] = true
		return nil, nil
	}

	state.active[channelID] = true
	if parentID, ok := transactionOutputSigningChannelID(channel); ok {
		parent, err := state.resolve(parentID)
		if err != nil {
			delete(state.active, channelID)
			return nil, err
		}
		channel.Channel = parent
	}
	delete(state.active, channelID)
	state.resolved[channelID] = true
	return channel, nil
}

func transactionOutputSigningChannelID(output *TransactionOutput) (string, bool) {
	if output == nil {
		return "", false
	}
	output = currentTransactionOutput(output)
	if !output.Script.IsClaimName() && !output.Script.IsUpdateClaim() {
		return "", false
	}
	claim, ok := decodeTransactionClaim(output.Script.Claim)
	if !ok || claim.ChannelID == nil || *claim.ChannelID == "" {
		return "", false
	}
	return *claim.ChannelID, true
}
