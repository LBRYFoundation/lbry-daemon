package wallet

import (
	"encoding/json"
	"errors"
	"fmt"
	"lbry/daemon/auth"
	"os"
	"path/filepath"
	"time"
)

// SetAccount ports lbry-sdk's account_set. Only the settings that account_set changes are written, into the
// wallet file as it is on disk: every other field of the wallet and of its accounts (seeds, private keys,
// preferences, channel keys, ...) is kept exactly as it was, so a locked wallet needs no password.
func (walletManager *DefaultWalletManager) SetAccount(user *auth.User, walletID string, accountID string, change AccountChange) (*Account, error) {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()

	wallet := walletManager.Get(nil, walletID)
	if wallet == nil {
		return nil, ErrWalletNotFound
	}
	index := -1
	for i := range wallet.Accounts {
		if wallet.Accounts[i].ID() == accountID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("Couldn't find account: %s.", accountID)
	}

	path := filepath.Join(walletManager.directory, wallet.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	var accounts []map[string]json.RawMessage
	if err := json.Unmarshal(file["accounts"], &accounts); err != nil {
		return nil, err
	}
	if len(accounts) != len(wallet.Accounts) {
		return nil, errors.New("The wallet file changed on disk, reload it first.")
	}

	account := wallet.Accounts[index]
	changed := false

	if account.AddressGeneratorDetails()["name"] == AddressGeneratorName {
		generator := account.AddressGeneratorDetails()
		for chain, settings := range map[string]struct{ gap, maxUses *int }{
			"change":    {change.ChangeGap, change.ChangeMaxUses},
			"receiving": {change.ReceivingGap, change.ReceivingMaxUses},
		} {
			values := generator[chain].(map[string]any)
			if settings.gap != nil {
				values["gap"] = *settings.gap
				changed = true
			}
			if settings.maxUses != nil {
				values["maximum_uses_per_address"] = *settings.maxUses
				changed = true
			}
		}
		if changed {
			raw, err := json.Marshal(generator)
			if err != nil {
				return nil, err
			}
			account.AddressGenerator = raw
		}
	}
	if change.NewName != nil {
		account.Name = *change.NewName
		changed = true
	}
	moveToFront := change.Default && index != 0
	if moveToFront {
		changed = true
	}
	if !changed {
		return &account, nil
	}
	account.ModifiedOn = float64(time.Now().Unix())

	patched := accounts[index]
	for key, value := range map[string]any{"name": account.Name, "modified_on": int64(account.ModifiedOn)} {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		patched[key] = raw
	}
	if account.AddressGenerator != nil {
		patched["address_generator"] = account.AddressGenerator
	}
	updated := append([]Account{}, wallet.Accounts...)
	updated[index] = account
	if moveToFront {
		accounts = append([]map[string]json.RawMessage{accounts[index]}, append(accounts[:index:index], accounts[index+1:]...)...)
		updated = append([]Account{updated[index]}, append(updated[:index:index], updated[index+1:]...)...)
	}
	file["accounts"], err = json.Marshal(accounts)
	if err != nil {
		return nil, err
	}
	output, err := json.MarshalIndent(file, "", "    ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomically(path, output); err != nil {
		return nil, err
	}
	wallet.Accounts = updated
	return &account, nil
}

// writeFileAtomically writes next to the file and renames over it, keeping the file's permissions, so a
// crash never leaves a half-written wallet.
func writeFileAtomically(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wallet-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
