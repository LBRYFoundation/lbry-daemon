package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
)

func (rpcServer *RPCServer) handleChannelSign(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager, wallet, ledger, err := rpcServer.channelUtilityContext(normalized)
	_ = manager
	if err != nil {
		panic(err)
	}
	if wallet.IsLocked() {
		panic(errors.New("Cannot spend funds with locked wallet, unlock first."))
	}
	channel, err := selectSigningChannel(normalized, ledger, wallet)
	if err != nil {
		panic(err)
	}
	data, err := hex.DecodeString(fmt.Sprint(normalized.named["hexdata"]))
	if err != nil {
		panic(err)
	}
	salt, ok := normalized.named["salt"].(string)
	if !ok {
		salt = fmt.Sprint(time.Now().Unix())
	}
	signature, err := walletpkg.SignChannelData(channel, data, salt)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, map[string]any{"signature": signature, "signing_ts": salt, "salt": salt})
}

func (rpcServer *RPCServer) handleChannelExport(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	_, wallet, ledger, err := rpcServer.channelUtilityContext(normalized)
	if err != nil {
		panic(err)
	}
	normalized.named["channel_account_id"] = normalized.named["account_id"]
	channel, err := selectSigningChannel(normalized, ledger, wallet)
	if err != nil {
		panic(err)
	}
	address, err := channel.Address(ledger.Network)
	if err != nil {
		panic(err)
	}
	holdingKey, err := walletpkg.ChannelHoldingPublicKey(ledger, wallet, channel)
	if err != nil || holdingKey == nil {
		panic(errors.New("Can't find public key for address holding the channel."))
	}
	privatePEM, err := channel.PrivateKey.ToPEM()
	if err != nil {
		panic(err)
	}
	claimID, err := channel.ClaimID()
	if err != nil {
		panic(err)
	}
	payload := struct {
		Name              string `json:"name"`
		ChannelID         string `json:"channel_id"`
		HoldingAddress    string `json:"holding_address"`
		HoldingPublicKey  string `json:"holding_public_key"`
		SigningPrivateKey string `json:"signing_private_key"`
	}{string(channel.Script.ClaimName), claimID, address, holdingKey.ExtendedKeyString(), privatePEM}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, keys.EncodeBase58(encoded))
}

func (rpcServer *RPCServer) handleChannelImport(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager, wallet, ledger, err := rpcServer.channelUtilityContext(normalized)
	if err != nil {
		panic(err)
	}
	encoded, ok := normalized.named["channel_data"].(string)
	if !ok {
		panic(errors.New("channel_data must be a string"))
	}
	raw, err := keys.DecodeBase58(encoded)
	if err != nil {
		panic(err)
	}
	var exported struct {
		Name              string `json:"name"`
		ChannelID         string `json:"channel_id"`
		HoldingAddress    string `json:"holding_address"`
		HoldingPublicKey  string `json:"holding_public_key"`
		SigningPrivateKey string `json:"signing_private_key"`
	}
	if err := json.Unmarshal(raw, &exported); err != nil {
		panic(err)
	}
	signingKey, err := keys.PrivateKeyFromPEM(ledger.Network, exported.SigningPrivateKey)
	if err != nil {
		panic(err)
	}
	account, err := channelHoldingAccount(normalized.ctx, wallet, exported.HoldingAddress)
	if err != nil {
		panic(err)
	}
	if account == nil {
		parsed, err := keys.ParseExtendedKey(ledger.Network, exported.HoldingPublicKey)
		if err != nil || parsed.IsPrivate() {
			panic(errors.New("exported holding public key is invalid"))
		}
		account, err = walletpkg.NewAccount(ledger.Network, walletpkg.NewObject(
			walletpkg.Member{Key: "name", Value: "Holding Account For Channel " + exported.Name},
			walletpkg.Member{Key: "public_key", Value: exported.HoldingPublicKey},
			walletpkg.Member{Key: "address_generator", Value: walletpkg.NewObject(
				walletpkg.Member{Key: "name", Value: walletpkg.SingleAddressGenerator},
			)},
		))
		if err != nil {
			panic(err)
		}
		if err := manager.RegisterAccount(ledger.Network.ID(), account); err != nil {
			panic(err)
		}
		wallet.AddAccount(account)
		if _, err := account.EnsureAddressGap(normalized.ctx); err != nil {
			panic(err)
		}
		addresses, err := account.Receiving.GetAddresses(normalized.ctx, false)
		if err != nil || len(addresses) == 0 || addresses[0] != exported.HoldingAddress {
			panic(errors.New("exported holding address does not match holding public key"))
		}
	}
	if err := account.AddChannelPrivateKey(signingKey); err != nil {
		panic(err)
	}
	if _, err := wallet.Save(); err != nil {
		panic(err)
	}
	sendResultResponse(response, "Added channel signing key for "+exported.Name+".")
}

func channelHoldingAccount(ctx context.Context, wallet *walletpkg.Wallet, address string) (*walletpkg.Account, error) {
	for _, account := range wallet.Accounts {
		if account == nil || account.Receiving == nil {
			continue
		}
		addresses, err := account.Receiving.GetAddresses(ctx, false)
		if err != nil {
			return nil, err
		}
		for _, candidate := range addresses {
			if candidate == address {
				return account, nil
			}
		}
	}
	return nil, nil
}

func (rpcServer *RPCServer) channelUtilityContext(
	normalized normalizedRPCParams,
) (*walletpkg.WalletManager, *walletpkg.Wallet, *walletpkg.Ledger, error) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		return nil, nil, nil, errors.New("wallet manager is unavailable")
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		return nil, nil, nil, err
	}
	wallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil || wallet == nil {
		return nil, nil, nil, errors.New("default wallet is unavailable")
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, nil, nil, errors.New("default ledger is unavailable")
	}
	return manager, wallet, ledger, nil
}
