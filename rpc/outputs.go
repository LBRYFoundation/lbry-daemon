package rpc

import "google.golang.org/protobuf/encoding/protowire"

// Hub Outputs protobuf types and decoders.

type HubOutput struct {
	TxHash []byte
	Nout   uint32
	Height uint32
	Meta   *HubClaimMeta
}

type HubClaimMeta struct {
	CanonicalURL    string
	ShortURL        string
	IsControlling   bool
	CreationHeight  uint32
	EffectiveAmount uint64
	SupportAmount   uint64
	ClaimsInChannel uint32
	Reposted        uint32
	ChannelTxHash   []byte
	ChannelNout     uint32
	HasChannel      bool
}

type HubOutputs struct {
	Txos      []HubOutput
	ExtraTxos []HubOutput
	Total     uint32
}

func decodeOutputs(data []byte) (*HubOutputs, error) {
	result := &HubOutputs{}
	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}
		switch {
		case typ == protowire.BytesType && num == 1: // txos
			val, _ := protowire.ConsumeBytes(b)
			result.Txos = append(result.Txos, decodeHubOutput(val))
		case typ == protowire.BytesType && num == 2: // extra_txos
			val, _ := protowire.ConsumeBytes(b)
			result.ExtraTxos = append(result.ExtraTxos, decodeHubOutput(val))
		case typ == protowire.VarintType && num == 3: // total
			val, _ := protowire.ConsumeVarint(b)
			result.Total = uint32(val)
		}
		b = b[fieldLen:]
	}
	return result, nil
}

func decodeHubOutput(data []byte) HubOutput {
	out := HubOutput{}
	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}
		switch {
		case typ == protowire.BytesType && num == 1: // tx_hash
			val, _ := protowire.ConsumeBytes(b)
			out.TxHash = make([]byte, len(val))
			copy(out.TxHash, val)
		case typ == protowire.VarintType && num == 2: // nout
			val, _ := protowire.ConsumeVarint(b)
			out.Nout = uint32(val)
		case typ == protowire.VarintType && num == 3: // height
			val, _ := protowire.ConsumeVarint(b)
			out.Height = uint32(val)
		case typ == protowire.BytesType && num == 7: // ClaimMeta
			val, _ := protowire.ConsumeBytes(b)
			out.Meta = decodeClaimMeta(val)
		}
		b = b[fieldLen:]
	}
	return out
}

func decodeClaimMeta(data []byte) *HubClaimMeta {
	meta := &HubClaimMeta{}
	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}
		switch {
		case typ == protowire.BytesType && num == 1: // channel reference
			val, _ := protowire.ConsumeBytes(b)
			ch := decodeHubOutput(val)
			if len(ch.TxHash) > 0 {
				meta.ChannelTxHash = ch.TxHash
				meta.ChannelNout = ch.Nout
				meta.HasChannel = true
			}
		case typ == protowire.BytesType && num == 3: // short_url
			val, _ := protowire.ConsumeBytes(b)
			meta.ShortURL = string(val)
		case typ == protowire.BytesType && num == 4: // canonical_url
			val, _ := protowire.ConsumeBytes(b)
			meta.CanonicalURL = string(val)
		case typ == protowire.VarintType && num == 5: // is_controlling
			val, _ := protowire.ConsumeVarint(b)
			meta.IsControlling = val != 0
		case typ == protowire.VarintType && num == 7: // creation_height
			val, _ := protowire.ConsumeVarint(b)
			meta.CreationHeight = uint32(val)
		case typ == protowire.VarintType && num == 10: // claims_in_channel
			val, _ := protowire.ConsumeVarint(b)
			meta.ClaimsInChannel = uint32(val)
		case typ == protowire.VarintType && num == 11: // reposted
			val, _ := protowire.ConsumeVarint(b)
			meta.Reposted = uint32(val)
		case typ == protowire.VarintType && num == 20: // effective_amount
			val, _ := protowire.ConsumeVarint(b)
			meta.EffectiveAmount = val
		case typ == protowire.VarintType && num == 21: // support_amount
			val, _ := protowire.ConsumeVarint(b)
			meta.SupportAmount = val
		}
		b = b[fieldLen:]
	}
	return meta
}
