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
			shortURL, ok := shortURLValue.([]uint8)
			if ok {
				claim["short_url"] = "lbry://" + string(shortURL)
			}
		}

		canonicalURLValue, canonicalURLOk := meta[4]
		if canonicalURLOk {
			canonicalURL, ok := canonicalURLValue.([]uint8)
			if ok {
				claim["canonical_url"] = "lbry://" + string(canonicalURL)
			}
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

	streamValue, streamOk := decodedProtobufClaimData[1]
	if streamOk {
		stream := streamValue.(map[int]any)

		sourceValue, sourceOk := stream[1]
		if sourceOk {
			source := map[string]any{}

			hashValue, hashOk := (sourceValue.(map[int]any))[1]
			if hashOk {
				hash := hex.EncodeToString(hashValue.([]uint8))
				source["hash"] = hash
			}

			nameValue, nameOk := (sourceValue.(map[int]any))[2]
			if nameOk {
				name := string(nameValue.([]uint8))
				source["name"] = name
			}

			mediaTypeValue, mediaTypeOk := (sourceValue.(map[int]any))[4]
			if mediaTypeOk {
				mediaType := string(mediaTypeValue.([]uint8))
				source["media_type"] = mediaType
			}

			urlValue, urlOk := (sourceValue.(map[int]any))[5]
			if urlOk {
				url := string(urlValue.([]uint8))
				source["url"] = url
			}

			sdHashValue, sdHashOk := (sourceValue.(map[int]any))[6]
			if sdHashOk {
				sdHash := hex.EncodeToString(sdHashValue.([]uint8))
				source["sd_hash"] = sdHash
			}

			claimValue["source"] = source
		}

		authorValue, authorOk := stream[2]
		if authorOk {
			author := string(authorValue.([]uint8))
			claimValue["author"] = author
		}

		licenseValue, licenseOk := stream[3]
		if licenseOk {
			license := string(licenseValue.([]uint8))
			claimValue["license"] = license
		}

		licenseURLValue, licenseURLOk := stream[4]
		if licenseURLOk {
			licenseURL := string(licenseURLValue.([]uint8))
			claimValue["license_url"] = licenseURL
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

			addressValue, addressOk := (feeValue.(map[int]any))[2]
			if addressOk {
				_ = addressValue.([]byte)
				//fee["address"] = address
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

	titleValue, titleOk := decodedProtobufClaimData[8]
	if titleOk {
		title := string(titleValue.([]uint8))
		claimValue["title"] = title
	}

	descriptionValue, descriptionOk := decodedProtobufClaimData[9]
	if descriptionOk {
		description := string(descriptionValue.([]uint8))
		claimValue["description"] = description
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
		_, ok := tagsValue.([]any)
		if ok {
			for _, tagValue := range tagsValue.([]any) {
				tag, ok := tagValue.([]uint8)
				if ok {
					tags = append(tags, string(tag))
				}
			}
			claimValue["tags"] = tags
		}
	}

	claim["value"] = claimValue

	_, hasClaimTypeStream := decodedProtobufClaimData[1]
	if hasClaimTypeStream {
		claim["value_type"] = "stream"
	}
	_, hasClaimTypeChannel := decodedProtobufClaimData[2]
	if hasClaimTypeChannel {
		claim["value_type"] = "channel"
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
