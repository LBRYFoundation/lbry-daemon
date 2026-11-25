package rpc

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"lbry/daemon/wallet"
)

// handleJSONRPCMessageChannelSign is lbry-sdk's channel_sign: sign data with the signing key of one of the
// wallet's channels.
func handleJSONRPCMessageChannelSign(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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
	channelName, err := GetStringParameter(paramsMap, "channel_name", false, "")
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	channelID, err := GetStringParameter(paramsMap, "channel_id", false, "")
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	hexData, err := GetStringParameter(paramsMap, "hexdata", true, "")
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	salt, err := GetStringParameter(paramsMap, "salt", false, "")
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	accountIDs, accountIDsOk := stringList(paramsMap["channel_account_id"])
	if !accountIDsOk {
		sendErrorResponse(w, 400, "Parameter 'channel_account_id' must be a string or a list of strings.")
		return
	}
	data, err := hex.DecodeString(hexData)
	if err != nil {
		sendErrorResponse(w, 400, "Parameter 'hexdata' must be hexadecimal.")
		return
	}

	// lbry-sdk answers a locked wallet with an assertion before it looks at anything else.
	if walletObj.IsLocked() {
		sendErrorResponse(w, 400, "Cannot spend funds with locked wallet, unlock first.")
		return
	}
	accounts := walletObj.Accounts
	if len(accountIDs) > 0 {
		accounts = nil
		for _, accountID := range accountIDs {
			account := walletObj.GetAccountByID(accountID)
			if account == nil {
				sendErrorResponse(w, 400, "Account does not exist.")
				return
			}
			accounts = append(accounts, account)
		}
	}

	var names, claimIDs []string
	if channelID != "" {
		claimIDs = []string{channelID}
	} else if channelName != "" {
		names = []string{channelName}
	}
	var channels []ownedChannel
	if len(claimIDs) > 0 || len(names) > 0 {
		channels, err = ownedChannels(rpcServer.hub, accounts, names, claimIDs)
		if err != nil {
			sendErrorResponse(w, 502, err.Error())
			return
		}
	}
	result, err := signWithChannel(channels, channelID, channelName, data, salt, time.Now())
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}
	sendResultResponse(w, result)
}

// signWithChannel signs the data with the one channel the wallet has under the id or name, and answers like
// lbry-sdk. Without a salt it uses the time, in seconds.
func signWithChannel(channels []ownedChannel, channelID string, channelName string, data []byte, salt string, now time.Time) (map[string]any, error) {
	var key, value string
	switch {
	case channelID != "":
		key, value = "id", channelID
	case channelName != "":
		key, value = "name", channelName
	default:
		return nil, errors.New("Couldn't find channel because a channel_id or channel_name was not provided.")
	}
	switch len(channels) {
	case 0:
		return nil, fmt.Errorf("Couldn't find channel with channel_%s '%s'.", key, value)
	case 1:
	default:
		return nil, fmt.Errorf("Multiple channels found with channel_%s '%s', pass a channel_id to narrow it down.", key, value)
	}

	channel := channels[0]
	privateKey, err := channel.account.ChannelPrivateKey(channelPublicKeyOf(channel.item))
	if err != nil {
		return nil, err
	}
	if privateKey == nil {
		return nil, fmt.Errorf("Couldn't find private key for %s '%s'.", key, value)
	}
	claimID, _ := channel.item["claim_id"].(string)
	claimHash, err := hex.DecodeString(claimID)
	if err != nil || len(claimHash) != 20 {
		return nil, errors.New("The channel has no valid claim id.")
	}
	// The claim id is the claim hash the other way round.
	for i, j := 0, len(claimHash)-1; i < j; i, j = i+1, j-1 {
		claimHash[i], claimHash[j] = claimHash[j], claimHash[i]
	}

	if salt == "" {
		salt = strconv.FormatInt(now.Unix(), 10)
	}
	return map[string]any{
		"signature":  wallet.SignChannelData(privateKey, claimHash, data, salt),
		"signing_ts": salt, // deprecated in lbry-sdk, kept because clients read it
		"salt":       salt,
	}, nil
}
