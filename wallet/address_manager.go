package wallet

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

var (
	ErrAddressManagerUnavailable = errors.New("address manager persistence is unavailable")
	ErrInvalidAddressGap         = errors.New("invalid address gap")
	ErrNoUsableAddress           = errors.New("address manager produced no usable address")
)

func (account *Account) AddressManagers() []*AddressManager {
	if account == nil || account.Receiving == nil {
		return []*AddressManager{}
	}
	managers := []*AddressManager{account.Receiving}
	if account.Change != nil && account.Change != account.Receiving {
		managers = append(managers, account.Change)
	}
	return managers
}

func (account *Account) EnsureAddressGap(ctx context.Context) ([]string, error) {
	addresses := make([]string, 0)
	for _, manager := range account.AddressManagers() {
		created, err := manager.EnsureAddressGap(ctx)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, created...)
	}
	return addresses, nil
}

func (manager *AddressManager) GetAddressRecords(
	ctx context.Context, onlyUsable bool,
) ([]ledgerdb.AddressRecord, error) {
	return manager.queryAddressRecords(ctx, onlyUsable, nil, ledgerdb.AddressOrderUnspecified)
}

func (manager *AddressManager) GetAddresses(
	ctx context.Context, onlyUsable bool,
) ([]string, error) {
	records, err := manager.GetAddressRecords(ctx, onlyUsable)
	if err != nil {
		return nil, err
	}
	addresses := make([]string, len(records))
	for index, record := range records {
		addresses[index] = record.Address
	}
	return addresses, nil
}

func (manager *AddressManager) GetOrCreateUsableAddress(ctx context.Context) (string, error) {
	if manager == nil {
		return "", ErrAddressManagerUnavailable
	}
	manager.addressGeneratorMu.Lock()
	limit := 10
	records, err := manager.queryAddressRecords(
		ctx, true, &limit, ledgerdb.AddressOrderUnspecified,
	)
	manager.addressGeneratorMu.Unlock()
	if err != nil {
		return "", err
	}
	if len(records) > 0 {
		choice, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(records))))
		if err != nil {
			return "", err
		}
		return records[choice.Int64()].Address, nil
	}
	created, err := manager.EnsureAddressGap(ctx)
	if err != nil {
		return "", err
	}
	if len(created) == 0 {
		return "", ErrNoUsableAddress
	}
	return created[0], nil
}

func (manager *AddressManager) queryAddressRecords(
	ctx context.Context,
	onlyUsable bool,
	limit *int,
	order ledgerdb.AddressOrder,
) ([]ledgerdb.AddressRecord, error) {
	if manager == nil || manager.account == nil || manager.account.ledger == nil ||
		manager.account.ledger.Database == nil {
		return nil, ErrAddressManagerUnavailable
	}
	query := ledgerdb.AddressQuery{
		Account: manager.account.ID,
		Chain:   &manager.ChainNumber,
		Limit:   limit,
		Order:   order,
	}
	if manager.singleAddress {
		// SingleKey ignores only_usable and does not install a default order.
		return manager.account.ledger.Database.GetAddresses(ctx, query)
	}
	if onlyUsable {
		maximumUses, err := addressManagerInteger(manager.MaximumUsesPerAddress)
		if err != nil {
			return nil, fmt.Errorf("%w: maximum_uses_per_address: %v", ErrInvalidAddressGap, err)
		}
		value := int64(maximumUses)
		query.UsedTimesLT = &value
	}
	if query.Order == ledgerdb.AddressOrderUnspecified {
		query.Order = ledgerdb.AddressOrderUsedTimesAscending
	}
	return manager.account.ledger.Database.GetAddresses(ctx, query)
}

func (manager *AddressManager) GetMaxGap(ctx context.Context) (int, error) {
	if manager == nil {
		return 0, ErrAddressManagerUnavailable
	}
	if manager.singleAddress {
		return 0, nil
	}
	records, err := manager.queryAddressRecords(
		ctx, false, nil, ledgerdb.AddressOrderIndexAscending,
	)
	if err != nil {
		return 0, err
	}
	maximumGap, currentGap := 0, 0
	for _, record := range records {
		if record.UsedTimes == 0 {
			currentGap++
			continue
		}
		if currentGap > maximumGap {
			maximumGap = currentGap
		}
		currentGap = 0
	}
	return maximumGap, nil
}

// SaveMaxGap preserves the largest observed unused runs before the initial
// synchronization, with the pinned minimum receiving/change gaps.
func (account *Account) SaveMaxGap(ctx context.Context) (bool, error) {
	if account == nil || account.GeneratorName != DeterministicChainGenerator ||
		account.Receiving == nil || account.Change == nil {
		return false, nil
	}
	receivingMax, err := account.Receiving.GetMaxGap(ctx)
	if err != nil {
		return false, err
	}
	changeMax, err := account.Change.GetMaxGap(ctx)
	if err != nil {
		return false, err
	}
	newReceiving, newChange := max(20, receivingMax+1), max(6, changeMax+1)
	changed := false
	account.Receiving.addressGeneratorMu.Lock()
	if current, currentErr := addressManagerInteger(account.Receiving.Gap); currentErr != nil || current != newReceiving {
		account.Receiving.Gap = newReceiving
		changed = true
	}
	account.Receiving.addressGeneratorMu.Unlock()
	account.Change.addressGeneratorMu.Lock()
	if current, currentErr := addressManagerInteger(account.Change.Gap); currentErr != nil || current != newChange {
		account.Change.Gap = newChange
		changed = true
	}
	account.Change.addressGeneratorMu.Unlock()
	if !changed {
		return false, nil
	}
	if account.wallet == nil {
		return true, ErrAccountWalletMissing
	}
	_, err = account.wallet.Save()
	return true, err
}

// EnsureAddressGap persists newly derived keys before announcing them. This
// ordering is observable: a failed subscription leaves the inserted rows.
func (manager *AddressManager) EnsureAddressGap(ctx context.Context) ([]string, error) {
	return manager.ensureAddressGap(ctx, func(addresses []string) error {
		if manager.account == nil || manager.account.ledger == nil {
			return ErrAddressManagerUnavailable
		}
		return manager.account.ledger.announceAddresses(ctx, manager, addresses)
	})
}

func (manager *AddressManager) ensureAddressGap(
	ctx context.Context, announce func([]string) error,
) ([]string, error) {
	if manager == nil || manager.account == nil || manager.account.ledger == nil ||
		manager.account.ledger.Database == nil || manager.PublicKey == nil {
		return nil, ErrAddressManagerUnavailable
	}
	manager.addressGeneratorMu.Lock()
	defer manager.addressGeneratorMu.Unlock()
	if manager.singleAddress {
		records, err := manager.queryAddressRecords(ctx, false, nil, ledgerdb.AddressOrderUnspecified)
		if err != nil {
			return nil, err
		}
		if len(records) > 0 {
			return []string{}, nil
		}
		if err := manager.persistPublicKeys(ctx, []*keys.PublicKey{manager.PublicKey}); err != nil {
			return nil, err
		}
		addresses := []string{manager.PublicKey.Address()}
		if announce != nil {
			if err := announce(addresses); err != nil {
				return nil, err
			}
		}
		return addresses, nil
	}

	gap, err := addressManagerInteger(manager.Gap)
	if err != nil {
		return nil, fmt.Errorf("%w: gap: %v", ErrInvalidAddressGap, err)
	}
	records, err := manager.queryAddressRecords(
		ctx, false, &gap, ledgerdb.AddressOrderIndexDescending,
	)
	if err != nil {
		return nil, err
	}
	existingGap := 0
	for _, record := range records {
		if record.UsedTimes != 0 {
			break
		}
		existingGap++
	}
	if existingGap == gap {
		return []string{}, nil
	}
	start := int64(0)
	if len(records) > 0 {
		start = records[0].N + 1
	}
	missing := gap - existingGap
	if missing <= 0 {
		return []string{}, nil
	}
	publicKeys := make([]*keys.PublicKey, 0, missing)
	for index := start; index < start+int64(missing); index++ {
		publicKey, err := manager.PublicKey.Child(index)
		if err != nil {
			return nil, err
		}
		publicKeys = append(publicKeys, publicKey)
	}
	if err := manager.persistPublicKeys(ctx, publicKeys); err != nil {
		return nil, err
	}
	addresses := make([]string, len(publicKeys))
	for index, publicKey := range publicKeys {
		addresses[index] = publicKey.Address()
	}
	if announce != nil {
		if err := announce(addresses); err != nil {
			return nil, err
		}
	}
	return addresses, nil
}

func (manager *AddressManager) persistPublicKeys(
	ctx context.Context, publicKeys []*keys.PublicKey,
) error {
	records := make([]ledgerdb.AddressKey, len(publicKeys))
	for index, publicKey := range publicKeys {
		chainCode := publicKey.ChainCode()
		records[index] = ledgerdb.AddressKey{
			Address:   publicKey.Address(),
			Chain:     manager.ChainNumber,
			PublicKey: publicKey.CompressedBytes(),
			ChainCode: append([]byte(nil), chainCode[:]...),
			N:         int64(publicKey.ChildNumber()),
			Depth:     int64(publicKey.Depth()),
		}
	}
	return manager.account.ledger.Database.AddKeys(ctx, manager.account.ID, records)
}

func addressManagerInteger(value any) (int, error) {
	switch typed := value.(type) {
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			return 0, fmt.Errorf("%T is not an integer", value)
		}
	case float32, float64, string:
		return 0, fmt.Errorf("%T is not an integer", value)
	case *big.Int:
		if typed == nil {
			return 0, errors.New("integer is nil")
		}
	case big.Int, bool:
	default:
		kind := reflect.ValueOf(value)
		if !kind.IsValid() || (kind.Kind() < reflect.Int || kind.Kind() > reflect.Uint64) {
			return 0, fmt.Errorf("%T is not an integer", value)
		}
	}
	integer, err := accountPythonInt(value)
	if err != nil || !integer.IsInt64() {
		return 0, fmt.Errorf("integer is outside the supported range")
	}
	converted := integer.Int64()
	if int64(int(converted)) != converted {
		return 0, fmt.Errorf("integer is outside the platform range")
	}
	return int(converted), nil
}
