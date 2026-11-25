package rpc

import (
	"encoding/hex"
	"slices"
)

func convertProtobufToClaim(protobuf map[int]any, transactions map[string]any) map[string]any {
	claim := map[string]any{}

	claim["type"] = "claim"

	// The hub's protobuf has the transaction hash in internal byte order. The caller has already reversed it
	// in place for the transaction lookup, so it is the txid as written now.
	var transactionID string
	var txHash []byte // internal byte order

	txidValue, txidOk := protobuf[1]
	if txidOk {
		txid := txidValue.([]byte)
		transactionID = hex.EncodeToString(txid)
		claim["txid"] = transactionID
		txHash = slices.Clone(txid)
		slices.Reverse(txHash)
	}

	// The hub leaves out a position of 0, as protobuf does for any zero value.
	noutValue, noutOk := protobuf[2]
	position, _ := noutValue.(uint64)
	nout := uint32(position)
	claim["nout"] = position

	transactionData := transactions[transactionID].([]any)
	transactionBytes := transactionData[0].(string)
	//transactionInfo := transactionData[1].(map[string]any)

	output, _ := parseTxOutputs(transactionBytes)
	var claimScript *ClaimScript

	// The claim is the output at its position; without one, the last claim script of the transaction.
	for i, o := range output {
		script, _ := parseClaimScript(o.Script)
		if script != nil && (claimScript == nil || !noutOk || i == int(nout)) {
			claimScript = script
		}
	}
	if claimScript == nil {
		return claim
	}

	// The claim data starts with a version byte: 0 is an unsigned claim and the protobuf follows, 1 is a claim
	// signed by a channel, with the channel's claim id (20 bytes) and the signature (64) before the protobuf.
	var claimProtobuf []byte
	switch {
	case len(claimScript.ClaimData) > 0 && claimScript.ClaimData[0] == 0:
		claimProtobuf = claimScript.ClaimData[1:]
	case len(claimScript.ClaimData) >= 85 && claimScript.ClaimData[0] == 1:
		claimProtobuf = claimScript.ClaimData[85:]
	default:
		// TODO: Older schemes
		return claim
	}

	decodedProtobufClaimData, _ := DecodeRawProto(claimProtobuf)

	claimID := claimScript.ClaimID
	if claimID == nil {
		claimID = ComputeClaimID(txHash, nout)
	} else {
		claimID = reverseBytes(claimID)
	}
	claim["claim_id"] = hex.EncodeToString(claimID)
	claim["name"] = claimScript.Name
	claim["permanent_url"] = "lbry://" + claimScript.Name + "#" + hex.EncodeToString(claimID)

	heightValue, heightOk := protobuf[3]
	if heightOk {
		height := heightValue.(uint64)
		claim["height"] = height
	}
	metaValue, metaOk := protobuf[7]
	if metaOk {
		meta := metaValue.(map[int]any)

		claimMeta := map[string]any{}

		if shortURL, ok := protoBytes(meta, 3); ok {
			claim["short_url"] = "lbry://" + string(shortURL)
		}
		if canonicalURL, ok := protoBytes(meta, 4); ok {
			claim["canonical_url"] = "lbry://" + string(canonicalURL)
		}

		isControllingValue, isControllingOk := meta[5]
		if isControllingOk {
			isControlling := isControllingValue.(uint64) != 0
			claimMeta["is_controlling"] = isControlling
		}

		takeOverHeightValue, takeOverHeightOk := meta[6]
		if takeOverHeightOk {
			takeOverHeight := takeOverHeightValue.(uint64)
			claimMeta["take_over_height"] = takeOverHeight
		}

		creationHeightValue, creationHeightOk := meta[7]
		if creationHeightOk {
			creationHeight := creationHeightValue.(uint64)
			claimMeta["creation_height"] = creationHeight
		}

		activationHeightValue, activationHeightOk := meta[7]
		if activationHeightOk {
			activationHeight := activationHeightValue.(uint64)
			claimMeta["activation_height"] = activationHeight
		}

		repostedValue, repostedOk := meta[11]
		if repostedOk {
			reposted := repostedValue.(uint64) != 0
			claimMeta["reposted"] = reposted
		}

		claim["meta"] = claimMeta

		// The channel that signed the claim, as the hub sends it: a reference to the channel's claim
		// output. Its claim is read from the same transaction batch, so the caller has to have asked for it.
		if channelRef, ok := meta[1].(map[int]any); ok {
			if channel := convertChannelReference(channelRef, transactions); channel != nil {
				claim["signing_channel"] = channel
				// The hub only names a channel for a claim whose signature it has checked.
				claim["is_channel_signature_valid"] = true
			}
		}
	}

	// Inflating below

	claimValue := map[string]any{}

	streamValue, streamOk := decodedProtobufClaimData[1]
	if streamOk {
		stream, _ := streamValue.(map[int]any)

		if source, ok := stream[1].(map[int]any); ok {
			sourceValue := map[string]any{}
			if hash, ok := protoBytes(source, 1); ok {
				sourceValue["hash"] = hex.EncodeToString(hash)
			}
			if name, ok := protoBytes(source, 2); ok {
				sourceValue["name"] = string(name)
			}
			if mediaType, ok := protoBytes(source, 4); ok {
				sourceValue["media_type"] = string(mediaType)
			}
			if url, ok := protoBytes(source, 5); ok {
				sourceValue["url"] = string(url)
			}
			if sdHash, ok := protoBytes(source, 6); ok {
				sourceValue["sd_hash"] = hex.EncodeToString(sdHash)
			}
			claimValue["source"] = sourceValue
		}

		if author, ok := protoBytes(stream, 2); ok {
			claimValue["author"] = string(author)
		}
		if license, ok := protoBytes(stream, 3); ok {
			claimValue["license"] = string(license)
		}
		if licenseURL, ok := protoBytes(stream, 4); ok {
			claimValue["license_url"] = string(licenseURL)
		}

		feeValue, feeOk := stream[6]
		if feeOk {
			fee := map[string]any{}

			currencyValue, currencyOk := (feeValue.(map[int]any))[1]
			if currencyOk {
				currency := currencyValue.(uint64)
				if currency == 0 {
					fee["currency"] = "UNKNOWN_CURRENCY"
				}
				if currency == 1 {
					fee["currency"] = "LBC"
				}
				if currency == 2 {
					fee["currency"] = "BTC"
				}
				if currency == 3 {
					fee["currency"] = "USD"
				}
			}

			amountValue, amountOk := (feeValue.(map[int]any))[3]
			if amountOk {
				amount := amountValue.(uint64)
				fee["amount"] = amount
			}

			claimValue["fee"] = fee
		}

		// Temporary
		claimValue["stream_type"] = "document"

		_, hasStreamTypeImage := stream[10]
		if hasStreamTypeImage {
			claimValue["stream_type"] = "image"
		}
		_, hasStreamTypeVideo := stream[11]
		if hasStreamTypeVideo {
			claimValue["stream_type"] = "video"
		}
		_, hasStreamTypeAudio := stream[11]
		if hasStreamTypeAudio {
			claimValue["stream_type"] = "audio"
		}
		_, hasStreamTypeSoftware := stream[13]
		if hasStreamTypeSoftware {
			claimValue["stream_type"] = "software"
		}
	}

	if title, ok := protoBytes(decodedProtobufClaimData, 8); ok {
		claimValue["title"] = string(title)
	}
	if description, ok := protoBytes(decodedProtobufClaimData, 9); ok {
		claimValue["description"] = string(description)
	}

	thumbnailValue, thumbnailOk := decodedProtobufClaimData[10]
	if thumbnailOk {
		thumbnail := map[string]any{}

		if thumbnailMessage, ok := thumbnailValue.(map[int]any); ok {
			if url, ok := protoBytes(thumbnailMessage, 5); ok {
				thumbnail["url"] = string(url)
			}
		}

		claimValue["thumbnail"] = thumbnail
	}

	if tags := protoBytesList(decodedProtobufClaimData, 11); len(tags) > 0 {
		claimValue["tags"] = tags
	}

	claim["value"] = claimValue

	_, hasClaimTypeStream := decodedProtobufClaimData[1]
	if hasClaimTypeStream {
		claim["value_type"] = "stream"
	}
	_, hasClaimTypeChannel := decodedProtobufClaimData[2]
	if hasClaimTypeChannel {
		claim["value_type"] = "channel"
		// The key the channel signs with, which is also how the wallet finds the private key.
		if channel, ok := decodedProtobufClaimData[2].(map[int]any); ok {
			if publicKey, ok := protoBytes(channel, 1); ok {
				claimValue["public_key"] = hex.EncodeToString(publicKey)
			}
		}
	}
	_, hasClaimTypeCollection := decodedProtobufClaimData[3]
	if hasClaimTypeCollection {
		claim["value_type"] = "collection"
	}
	_, hasClaimTypeRepost := decodedProtobufClaimData[4]
	if hasClaimTypeRepost {
		claim["value_type"] = "repost"
	}

	// 	claim["_1"] = protobuf
	// 	claim["_2"] = decodedProtobufClaimData

	return claim
}

// channelReferenceTxID is the id of the transaction holding a channel the hub names in a claim's metadata,
// reversed in place into the order it is written, as the claim's own is. It is empty without one.
func channelReferenceTxID(protobuf map[int]any) string {
	meta, ok := protobuf[7].(map[int]any)
	if !ok {
		return ""
	}
	channelRef, ok := meta[1].(map[int]any)
	if !ok {
		return ""
	}
	txHash, ok := channelRef[1].([]byte)
	if !ok {
		return ""
	}
	slices.Reverse(txHash)
	return hex.EncodeToString(txHash)
}

// convertChannelReference reads the claim of a channel the hub names in another claim's metadata. It is nil
// when the channel's transaction was not fetched.
func convertChannelReference(reference map[int]any, transactions map[string]any) map[string]any {
	txid, ok := reference[1].([]byte)
	if !ok {
		return nil
	}
	if _, ok := transactions[hex.EncodeToString(txid)].([]any); !ok {
		return nil
	}
	return convertProtobufToClaim(reference, transactions)
}

// protoBytes is the bytes of a length-delimited field. DecodeRawProto reads such a field as a nested message
// when it happens to parse as one (a short title can), so it keeps the original bytes beside it.
func protoBytes(message map[int]any, field int) ([]byte, bool) {
	switch value := message[field].(type) {
	case []byte:
		return value, true
	case []any:
		// A repeated field: the last value counts, as in protobuf.
		if raw, ok := message[-field].([]any); ok && len(raw) > 0 {
			last, ok := raw[len(raw)-1].([]byte)
			return last, ok
		}
		return nil, false
	case nil:
		return nil, false
	}
	raw, ok := message[-field].([]byte)
	return raw, ok
}

// protoBytesList is the strings of a repeated length-delimited field.
func protoBytesList(message map[int]any, field int) []string {
	var list []string
	switch raw := message[-field].(type) {
	case []byte:
		list = append(list, string(raw))
	case []any:
		for _, element := range raw {
			if bytes, ok := element.([]byte); ok {
				list = append(list, string(bytes))
			}
		}
	}
	return list
}
