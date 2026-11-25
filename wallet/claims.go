package wallet

// ClaimOutput is an unspent claim output (a claim or an update of one) at one of an account's addresses.
type ClaimOutput struct {
	AccountID string
	TxID      string // hex, as the wallet server writes it
	Index     uint32
	Amount    int64  // dewies
	Address   string // the address holding it
	Script    []byte // the claim script
}

// ClaimOutputs lists the unspent claim outputs of the accounts, with the hub. Like WalletBalance it only
// needs public keys, and a hub failure is an error. Supports are not claims and are left out.
func ClaimOutputs(hub Hub, accounts []*Account, confirmations int) ([]ClaimOutput, error) {
	results, err := scanAccounts(hub, accounts, confirmations)
	if err != nil {
		return nil, err
	}
	type found struct {
		unspentOutput
		account int
	}
	var all []found
	var txids []string
	seen := map[string]bool{}
	for slot, result := range results {
		for _, output := range result.unspent {
			all = append(all, found{output, slot / 2})
			if !seen[output.txid] {
				seen[output.txid] = true
				txids = append(txids, output.txid)
			}
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	transactions, err := fetchTransactions(hub, txids)
	if err != nil {
		return nil, err
	}
	var claims []ClaimOutput
	for _, output := range all {
		tx := transactions[output.txid]
		if int(output.index) >= len(tx.outputs) {
			continue
		}
		spent := tx.outputs[output.index]
		if !spent.isClaim() {
			continue
		}
		address := ""
		if hash := spent.hash160(); hash != nil {
			address = base58CheckEncode(append([]byte{pubkeyAddressPrefix}, hash...))
		}
		claims = append(claims, ClaimOutput{
			AccountID: accounts[output.account].ID,
			TxID:      output.txid,
			Index:     output.index,
			Amount:    output.value,
			Address:   address,
			Script:    spent.script,
		})
	}
	return claims, nil
}

// HasKey tells whether the account holds the private key of the channel at the address, which is what lbry-sdk's
// has_signing_key reports.
func (account *Account) HasKey(address string) bool {
	_, has := account.Certificates[address]
	return has
}
