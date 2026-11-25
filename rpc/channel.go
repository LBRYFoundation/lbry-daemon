package rpc

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"slices"
	"sort"

	"lbry/daemon/wallet"
)

// hubClaimSearchLimit is the most claims the wallet server answers for one search.
const hubClaimSearchLimit = 50

// hubClaimSearch asks the wallet server for claims and turns them into lbry-sdk's claim objects. The total is
// only known when the hub said it.
func hubClaimSearch(params any) ([]map[string]any, uint64, bool, error) {
	searchResp, err := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      rand.Int() + 1,
		"method":  "blockchain.claimtrie.search",
		"params":  params,
	})
	if err != nil {
		return nil, 0, false, errors.New("The hub did not answer: " + err.Error())
	}
	encoded, encodedOk := searchResp["result"].(string)
	if !encodedOk {
		return nil, 0, false, errors.New("The hub sent an unexpected reply to the search.")
	}

	decodedBase64, _ := base64.StdEncoding.DecodeString(encoded)
	decodedProtobuf, _ := DecodeRawProto(decodedBase64)

	claims, ok := decodedProtobuf[1].([]any)

	txIDs := []string{}
	if ok {
		wanted := map[string]bool{}
		want := func(txid string) {
			if txid != "" && !wanted[txid] {
				wanted[txid] = true
				txIDs = append(txIDs, txid)
			}
		}
		for _, claim := range claims {
			claimMap := claim.(map[int]any)
			txidValue, txidOk := claimMap[1]
			if txidOk {
				txidBytes := txidValue.([]byte)
				slices.Reverse(txidBytes)
				want(hex.EncodeToString(txidBytes))
			}
			// The channel that signed the claim is read from its transaction too.
			want(channelReferenceTxID(claimMap))
		}
	}

	transactionData := map[string]any{}
	if len(txIDs) > 0 {
		transactionResp, err := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.transaction.get_batch",
			"params":  txIDs,
		})
		if err != nil {
			return nil, 0, false, errors.New("The hub did not answer: " + err.Error())
		}
		var transactionOk bool
		if transactionData, transactionOk = transactionResp["result"].(map[string]any); !transactionOk {
			return nil, 0, false, errors.New("The hub sent an unexpected reply to the transaction request.")
		}
	}

	items := []map[string]any{}
	if ok {
		for _, claim := range claims {
			if item := convertClaimSafely(claim.(map[int]any), transactionData); item != nil {
				items = append(items, item)
			}
		}
	}
	totalItems, okTotal := (decodedProtobuf[3]).(uint64)
	return items, totalItems, okTotal, nil
}

// convertClaimSafely converts one claim of a search, or returns nil and logs when its data is not what the
// converter expects, so one odd claim cannot take the whole answer down.
func convertClaimSafely(claim map[int]any, transactions map[string]any) (item map[string]any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("Skipping a claim that could not be read: %v", recovered)
			item = nil
		}
	}()
	return convertProtobufToClaim(claim, transactions)
}

// stringList reads a parameter that is a string or a list of strings, like lbry-sdk's `--name=<name>...`.
func stringList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, true
	case string:
		return []string{typed}, true
	case []any:
		list := make([]string, 0, len(typed))
		for _, item := range typed {
			text, isString := item.(string)
			if !isString {
				return nil, false
			}
			list = append(list, text)
		}
		return list, true
	}
	return nil, false
}

// claimIDOf is the claim id (display order) of a claim output: the id in an update's script, or the one the
// claim got when it was created, from its transaction and position.
func claimIDOf(output wallet.ClaimOutput) (string, string, bool) {
	script, err := parseClaimScript(output.Script)
	if err != nil {
		return "", "", false
	}
	if script.IsUpdate {
		return claimIDFromBytes(script.ClaimID), script.Name, true
	}
	txHash, err := hex.DecodeString(output.TxID)
	if err != nil {
		return "", "", false
	}
	return hex.EncodeToString(ComputeClaimID(reverseBytes(txHash), output.Index)), script.Name, true
}

func handleJSONRPCMessageChannelList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if !authenticated {
		handleUnauthorized(w)
		return
	}
	paramsMap, paramsMapOk := params.(map[string]any)
	if !paramsMapOk {
		paramsMap = map[string]any{}
	}
	walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	walletObj := rpcServer.walletManager.Read(user, walletID)
	if walletObj == nil {
		sendErrorResponse(w, 400, "Wallet does not exist.")
		return
	}
	isSpent, err := GetBooleanParameter(paramsMap, "is_spent", false, false)
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	if isSpent {
		sendErrorResponse(w, 501, "Parameter 'is_spent' is not supported yet: the daemon only knows the unspent channels.")
		return
	}
	noTotals, err := GetBooleanParameter(paramsMap, "no_totals", false, false)
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	page, err := GetIntegerParameter(paramsMap, "page", false, 1)
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	pageSize, err := GetIntegerParameter(paramsMap, "page_size", false, 0)
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	names, namesOk := stringList(paramsMap["name"])
	claimIDs, claimIDsOk := stringList(paramsMap["claim_id"])
	if !namesOk || !claimIDsOk {
		sendErrorResponse(w, 400, "Parameters 'name' and 'claim_id' must be strings or lists of strings.")
		return
	}

	accounts := walletObj.Accounts
	if HasParameter(paramsMap, "account_id") {
		accountID, err := GetStringParameter(paramsMap, "account_id", true, "")
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		account := walletObj.GetAccountByID(accountID)
		if account == nil {
			sendErrorResponse(w, 400, "Account does not exist.")
			return
		}
		accounts = []*wallet.Account{account}
	}

	outputs, err := wallet.ClaimOutputs(rpcServer.hub, accounts, 0)
	if err != nil {
		sendErrorResponse(w, 502, "Could not list the claims with the wallet server: "+err.Error())
		return
	}
	type mine struct {
		output  wallet.ClaimOutput
		claimID string
		name    string
	}
	byID := map[string]mine{}
	var ids []string
	for _, output := range outputs {
		claimID, name, valid := claimIDOf(output)
		if !valid || (len(names) > 0 && !slices.Contains(names, name)) || (len(claimIDs) > 0 && !slices.Contains(claimIDs, claimID)) {
			continue
		}
		if _, dup := byID[claimID]; !dup {
			ids = append(ids, claimID)
		}
		byID[claimID] = mine{output, claimID, name}
	}

	// The wallet server knows which of them are channels, and has the details of the confirmed ones.
	items := []map[string]any{}
	for start := 0; start < len(ids); start += hubClaimSearchLimit {
		chunk := ids[start:min(start+hubClaimSearchLimit, len(ids))]
		found, _, _, err := hubClaimSearch(map[string]any{"claim_id": chunk, "claim_type": "channel", "page_size": len(chunk)})
		if err != nil {
			sendErrorResponse(w, 502, err.Error())
			return
		}
		for _, item := range found {
			claimID, _ := item["claim_id"].(string)
			owned, has := byID[claimID]
			if !has {
				continue
			}
			item["name"] = owned.name
			item["permanent_url"] = "lbry://" + owned.name + "#" + claimID
			item["amount"] = wallet.FormatLBC(owned.output.Amount)
			item["address"] = owned.output.Address
			item["is_spent"] = false
			item["is_my_output"] = true
			for _, account := range accounts {
				if account.ID == owned.output.AccountID {
					item["has_signing_key"] = account.HasKey(owned.output.Address)
				}
			}
			items = append(items, item)
		}
	}
	// Newest first, as lbry-sdk lists outputs.
	sort.SliceStable(items, func(i, j int) bool {
		a, _ := items[i]["height"].(uint64)
		b, _ := items[j]["height"].(uint64)
		return a > b
	})

	result := Paginate(items, uint(max(page, 0)), uint(max(pageSize, 0))).ToMap()
	if noTotals {
		delete(result, "total_items")
		delete(result, "total_pages")
	}
	sendResultResponse(w, result)
}
