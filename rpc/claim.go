package rpc

import "encoding/hex"
import "slices"

func convertProtobufToClaim(protobuf map[int]any, transactions map[string]any) map[string]any {
	claim := map[string]any{}

	claim["type"] = "claim"

	var transactionID string

	txidValue, txidOk := protobuf[1]
	if txidOk {
		txid := txidValue.([]byte)
		transactionID = hex.EncodeToString(txid)
		slices.Reverse(txid)
		claim["txid"] = hex.EncodeToString(txid)
	}

	transactionData := transactions[transactionID].([]any)
	transactionBytes := transactionData[0].(string)
	//transactionInfo := transactionData[1].(map[string]any)

	output, _ := parseTxOutputs(transactionBytes)
	var claimScript *ClaimScript

	for _, o := range output {
		script, _ := parseClaimScript(o.Script)
		if script != nil {
			claimScript = script
		}
	}

	decodedProtobufClaimData, _ := DecodeRawProto(claimScript.ClaimData[85:])

	claimID := claimScript.ClaimID
	slices.Reverse(claimID)
	claim["claim_id"] = hex.EncodeToString(claimID)

	heightValue, heightOk := protobuf[3]
	if heightOk {
		height := heightValue.(uint64)
		claim["height"] = height
	}
	metaValue, metaOk := protobuf[7]
	if metaOk {
		meta := metaValue.(map[int]any)

		claimMeta := map[string]any{}

		shortURLValue, shortURLOk := meta[3]
		if shortURLOk {
			shortURL := string(shortURLValue.([]uint8))
			claim["short_url"] = "lbry://" + shortURL
		}

		canonicalURLValue, canonicalURLOk := meta[4]
		if canonicalURLOk {
			canonicalURL := string(canonicalURLValue.([]uint8))
			claim["canonical_url"] = "lbry://" + canonicalURL
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
	}

	// Inflating below

	claimValue := map[string]any{}

	titleValue, titleOk := decodedProtobufClaimData[8]
	if titleOk {
		title := string(titleValue.([]uint8))
		claimValue["title"] = title
	}

	thumbnailValue, thumbnailOk := decodedProtobufClaimData[10]
	if thumbnailOk {
		thumbnail := map[string]any{}

		urlValue, urlOk := (thumbnailValue.(map[int]any))[5]
		if urlOk {
			url := string(urlValue.([]uint8))
			thumbnail["url"] = url
		}

		claimValue["thumbnail"] = thumbnail
	}

	tagsValue, tagsOk := decodedProtobufClaimData[11]
	if tagsOk {
		var tags []string
		for _, tagValue := range tagsValue.([]any) {
			tags = append(tags, string(tagValue.([]uint8)))
		}
		claimValue["tags"] = tags
	}

	claim["value"] = claimValue

	return claim
}
