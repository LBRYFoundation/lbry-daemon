package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
)

func TestChannelSignUsesSaltClaimHashAndData(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	channel, channelID, _ := persistChannelUpdateFixture(t, &fixture)
	result := fileMutationRPCResult(t, fixture.server, "channel_sign", map[string]any{
		"channel_id": channelID, "hexdata": "010203", "salt": "fixed-salt",
	})
	encoded := result.(map[string]any)
	if encoded["salt"] != "fixed-salt" || encoded["signing_ts"] != "fixed-salt" {
		t.Fatalf("channel sign result = %#v", encoded)
	}
	signature, err := hex.DecodeString(encoded["signature"].(string))
	if err != nil || len(signature) != keys.CompactSignatureLength {
		t.Fatalf("signature = %x, %v", signature, err)
	}
	claimHash, err := hex.DecodeString(channelID)
	if err != nil {
		t.Fatal(err)
	}
	for left, right := 0, len(claimHash)-1; left < right; left, right = left+1, right-1 {
		claimHash[left], claimHash[right] = claimHash[right], claimHash[left]
	}
	preimage := append([]byte("fixed-salt"), claimHash...)
	preimage = append(preimage, 1, 2, 3)
	digest := sha256.Sum256(preimage)
	channelValue, err := walletpkg.DecodeClaimValue(channel.Outputs[0].Script.Claim)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := walletpkg.ClaimChannelPublicKey(channelValue)
	valid, verifyErr := keys.VerifyCompactSignature(publicKey, signature, digest[:])
	if err != nil || verifyErr != nil || !valid {
		t.Fatalf("channel signature valid = %v, errors %v / %v", valid, err, verifyErr)
	}
}

func TestChannelExportContainsHoldingAndSigningKeys(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	channel, channelID, _ := persistChannelUpdateFixture(t, &fixture)
	result := fileMutationRPCResult(t, fixture.server, "channel_export", map[string]any{
		"channel_id": channelID,
	})
	raw, err := keys.DecodeBase58(result.(string))
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		Name, ChannelID, HoldingAddress, HoldingPublicKey, SigningPrivateKey string
	}
	var object map[string]string
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	exported.Name = object["name"]
	exported.ChannelID = object["channel_id"]
	exported.HoldingAddress = object["holding_address"]
	exported.HoldingPublicKey = object["holding_public_key"]
	exported.SigningPrivateKey = object["signing_private_key"]
	if exported.Name != "@owned" || exported.ChannelID != channelID ||
		exported.HoldingAddress == "" || exported.HoldingPublicKey == "" || exported.SigningPrivateKey == "" {
		t.Fatalf("channel export = %#v", object)
	}
	signingKey, err := keys.PrivateKeyFromPEM(keys.MainNet, exported.SigningPrivateKey)
	channelValue, claimErr := walletpkg.DecodeClaimValue(channel.Outputs[0].Script.Claim)
	publicKey, publicErr := walletpkg.ClaimChannelPublicKey(channelValue)
	if err != nil || claimErr != nil || publicErr != nil ||
		hex.EncodeToString(signingKey.PublicKey().CompressedBytes()) != hex.EncodeToString(publicKey) {
		t.Fatalf("exported signing key mismatch: %v / %v / %v", err, claimErr, publicErr)
	}
	parsedHolding, err := keys.ParseExtendedKey(keys.MainNet, exported.HoldingPublicKey)
	if err != nil || parsedHolding.IsPrivate() {
		t.Fatalf("holding public key = %#v, %v", parsedHolding, err)
	}
	imported := fileMutationRPCResult(t, fixture.server, "channel_import", map[string]any{
		"channel_data": result,
	})
	if imported != "Added channel signing key for @owned." {
		t.Fatalf("channel import = %#v", imported)
	}
	stored, exists := fixture.account.ChannelKeys.Get(signingKey.Address())
	if !exists || stored != exported.SigningPrivateKey {
		t.Fatalf("imported channel key = %#v, exists %v", stored, exists)
	}
	manager := fixture.server.walletManagerProvider()
	unrelatedRoot, err := keys.PrivateKeyFromSeed(keys.MainNet, []byte("unrelated holding account"))
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := walletpkg.NewAccount(keys.MainNet, walletpkg.NewObject(
		walletpkg.Member{Key: "public_key", Value: unrelatedRoot.PublicKey().ExtendedKeyString()},
		walletpkg.Member{Key: "address_generator", Value: walletpkg.NewObject(
			walletpkg.Member{Key: "name", Value: walletpkg.SingleAddressGenerator},
		)},
	))
	if err != nil {
		t.Fatal(err)
	}
	importWallet := walletpkg.NewWallet(
		walletpkg.WithWalletName("imported"), walletpkg.WithWalletAccounts([]*walletpkg.Account{unrelated}),
	)
	fixture.ledger.SPVNetwork = nil
	if err := manager.RegisterAccount(keys.MainNet.ID(), unrelated); err != nil {
		t.Fatal(err)
	}
	if _, err := unrelated.EnsureAddressGap(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.Wallets = []*walletpkg.Wallet{importWallet}
	importServer := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager { return manager }))
	imported = fileMutationRPCResult(t, importServer, "channel_import", map[string]any{"channel_data": result})
	if imported != "Added channel signing key for @owned." || len(importWallet.Accounts) != 2 {
		t.Fatalf("read-only import = %#v, accounts %d", imported, len(importWallet.Accounts))
	}
	holdingAccount := importWallet.Accounts[1]
	addresses, err := holdingAccount.Receiving.GetAddresses(context.Background(), false)
	readOnlyKey, readOnlyExists := holdingAccount.ChannelKeys.Get(signingKey.Address())
	if err != nil || len(addresses) != 1 || addresses[0] != exported.HoldingAddress ||
		!readOnlyExists || readOnlyKey != exported.SigningPrivateKey {
		t.Fatalf("read-only holding account = addresses %v, key %#v/%v, error %v", addresses, readOnlyKey, readOnlyExists, err)
	}
}
