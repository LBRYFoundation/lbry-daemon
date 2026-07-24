package rpc

import (
	"testing"

	walletpkg "lbry/daemon/wallet"
)

func TestStreamRepostSignsWithOwnedChannel(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	channel, channelID, _ := persistChannelUpdateFixture(t, &fixture)
	fixture.network.downloadComplete = nil
	fileMutationRPCResult(t, fixture.server, "stream_repost", map[string]any{
		"name": "mirror", "bid": "1.0", "claim_id": fixture.claimID,
		"title": "Mirror", "channel_id": channelID,
	})
	fixture.network.mu.Lock()
	broadcasts := append([]string(nil), fixture.network.broadcasts...)
	fixture.network.mu.Unlock()
	if len(broadcasts) != 1 {
		t.Fatalf("repost broadcasts = %v", broadcasts)
	}
	transaction, err := walletpkg.ParseTransaction(mustDecodeHex(t, broadcasts[0]))
	value, decodeErr := walletpkg.DecodeClaimValue(transaction.Outputs[0].Script.Claim)
	channelValue, channelErr := walletpkg.DecodeClaimValue(channel.Outputs[0].Script.Claim)
	valid, verifyErr := walletpkg.VerifyTransactionClaimSignature(value, transaction, channelValue)
	if err != nil || decodeErr != nil || channelErr != nil || verifyErr != nil || !valid ||
		value.Type != "repost" || value.Value["claim_id"] != fixture.claimID || value.Value["title"] != "Mirror" {
		t.Fatalf("signed repost = %#v, valid %v, errors %v / %v / %v / %v", value, valid, err, decodeErr, channelErr, verifyErr)
	}
}
