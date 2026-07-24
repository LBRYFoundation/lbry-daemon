package wallet

import (
	"math/big"
	"strings"
)

// legacyTransactionClaimMeta mirrors JSONResponseEncoder.encode_claim_meta on
// the throwaway copy made by encode_output.
func (ledger *Ledger) legacyTransactionClaimMeta(source map[string]any) map[string]any {
	meta := make(map[string]any, len(source)+1)
	for key, value := range source {
		if strings.HasSuffix(key, "_amount") {
			if integer, ok := transactionMetaInteger(value); ok {
				value = transactionHistoryDewies(integer)
			}
		}
		meta[key] = value
	}
	if ledger == nil || ledger.Headers == nil {
		return meta
	}
	creationHeight, ok := transactionMetaHeight(meta["creation_height"])
	if !ok || creationHeight <= 0 || creationHeight > int64(ledger.Headers.Height()) {
		return meta
	}
	if timestamp, ok := ledger.Headers.EstimatedTimestamp(int(creationHeight), true); ok {
		meta["creation_timestamp"] = timestamp
	}
	return meta
}

func (ledger *Ledger) legacyTransactionClaimMetaRelations(
	meta map[string]any,
	options LegacyTransactionJSONOptions,
	active map[*TransactionOutput]struct{},
) (map[string]any, error) {
	encoded := make(map[string]any, len(meta))
	for key, value := range meta {
		normalized, err := ledger.legacyTransactionClaimMetaValue(value, options, active)
		if err != nil {
			return nil, err
		}
		encoded[key] = normalized
	}
	return encoded, nil
}

func (ledger *Ledger) legacyTransactionClaimMetaValue(
	value any,
	options LegacyTransactionJSONOptions,
	active map[*TransactionOutput]struct{},
) (any, error) {
	switch typed := value.(type) {
	case *TransactionOutput:
		if typed == nil {
			return nil, nil
		}
		return ledger.legacyTransactionOutputJSONState(
			currentTransactionOutput(typed), options, true, active,
		)
	case map[string]any:
		return ledger.legacyTransactionClaimMetaRelations(typed, options, active)
	case []any:
		encoded := make([]any, len(typed))
		for index, item := range typed {
			var err error
			encoded[index], err = ledger.legacyTransactionClaimMetaValue(item, options, active)
			if err != nil {
				return nil, err
			}
		}
		return encoded, nil
	case []*TransactionOutput:
		encoded := make([]any, len(typed))
		for index, output := range typed {
			var err error
			encoded[index], err = ledger.legacyTransactionClaimMetaValue(output, options, active)
			if err != nil {
				return nil, err
			}
		}
		return encoded, nil
	default:
		return value, nil
	}
}

func transactionMetaInteger(value any) (*big.Int, bool) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return big.NewInt(1), true
		}
		return big.NewInt(0), true
	case int:
		return big.NewInt(int64(typed)), true
	case int8:
		return big.NewInt(int64(typed)), true
	case int16:
		return big.NewInt(int64(typed)), true
	case int32:
		return big.NewInt(int64(typed)), true
	case int64:
		return big.NewInt(typed), true
	case uint:
		return new(big.Int).SetUint64(uint64(typed)), true
	case uint8:
		return new(big.Int).SetUint64(uint64(typed)), true
	case uint16:
		return new(big.Int).SetUint64(uint64(typed)), true
	case uint32:
		return new(big.Int).SetUint64(uint64(typed)), true
	case uint64:
		return new(big.Int).SetUint64(typed), true
	case uintptr:
		return new(big.Int).SetUint64(uint64(typed)), true
	case *big.Int:
		if typed != nil {
			return new(big.Int).Set(typed), true
		}
	}
	return nil, false
}

func transactionMetaHeight(value any) (int64, bool) {
	integer, ok := transactionMetaInteger(value)
	if !ok || !integer.IsInt64() {
		return 0, false
	}
	return integer.Int64(), true
}
