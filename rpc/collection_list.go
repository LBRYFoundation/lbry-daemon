package rpc

import (
	"encoding/json"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleCollectionList(w http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager, _, walletID, accountID := rpcServer.txoSelection(
		normalized, "get_collections", false,
	)
	resolveClaims := collectionResolveClaimsPlan{}
	if value, exists := normalized.named["resolve_claims"]; exists {
		resolveClaims = collectionListResolveClaimsPlan(value)
	}
	pagination, err := transactionListPaginationParameters(normalized.named)
	if err != nil {
		panic(err)
	}
	result, err := manager.GetCollectionPage(normalized.ctx, walletpkg.CollectionPageOptions{
		AccountID:              accountID,
		WalletID:               walletID,
		Page:                   pagination.page,
		PageSize:               pagination.pageSize,
		Offset:                 &pagination.offset,
		Resolve:                transactionListTruthy(normalized.named["resolve"]),
		ResolveClaimsEnabled:   resolveClaims.enabled,
		ResolveClaimsLimit:     resolveClaims.limit,
		ResolveClaimsError:     resolveClaims.comparisonErr,
		ResolveClaimsItemError: resolveClaims.itemErr,
	})
	if err != nil {
		panic(txoListRPCError(err, "get_collections"))
	}
	if result.Ledger == nil {
		panic(transactionListNoneAttributeError("get_collections"))
	}
	items := make([]any, len(result.Items))
	for index, output := range result.Items {
		encoded, encodeErr := result.Ledger.LegacyTransactionOutputJSONWithOptions(
			output,
			walletpkg.LegacyTransactionJSONOptions{
				IncludeProtobuf: transactionListTruthy(normalized.includeProtobuf),
			},
		)
		if encodeErr != nil {
			panic(encodeErr)
		}
		items[index] = encoded
	}
	wire := map[string]any{
		"items": items, "page": pagination.wirePage, "page_size": pagination.wirePageSize,
	}
	if result.TotalItems != nil {
		wire["total_items"] = *result.TotalItems
		wire["total_pages"] = transactionListPythonTotalPages(
			*result.TotalItems, pagination.pageSizeNumber,
		)
	}
	sendResultResponse(w, wire)
}

type collectionResolveClaimsPlan struct {
	enabled       bool
	limit         int
	comparisonErr error
	itemErr       error
}

func collectionListResolveClaimsPlan(value any) collectionResolveClaimsPlan {
	switch typed := value.(type) {
	case bool:
		if typed {
			return collectionResolveClaimsPlan{enabled: true, limit: 1}
		}
		return collectionResolveClaimsPlan{}
	case json.Number:
		if !strings.ContainsAny(typed.String(), ".eE") {
			integer, ok := new(big.Int).SetString(typed.String(), 10)
			if ok {
				if integer.Sign() <= 0 {
					return collectionResolveClaimsPlan{}
				}
				return collectionResolveClaimsPlan{
					enabled: true, limit: collectionListPositiveBigInt(integer),
				}
			}
		}
		floating, parseErr := strconv.ParseFloat(typed.String(), 64)
		if parseErr == nil || math.IsInf(floating, 0) {
			return collectionListFloatingResolveClaims(floating)
		}
	case int:
		return collectionListSignedResolveClaims(int64(typed))
	case int8:
		return collectionListSignedResolveClaims(int64(typed))
	case int16:
		return collectionListSignedResolveClaims(int64(typed))
	case int32:
		return collectionListSignedResolveClaims(int64(typed))
	case int64:
		return collectionListSignedResolveClaims(typed)
	case uint:
		return collectionListUnsignedResolveClaims(uint64(typed))
	case uint8:
		return collectionListUnsignedResolveClaims(uint64(typed))
	case uint16:
		return collectionListUnsignedResolveClaims(uint64(typed))
	case uint32:
		return collectionListUnsignedResolveClaims(uint64(typed))
	case uint64:
		return collectionListUnsignedResolveClaims(typed)
	case float32:
		return collectionListFloatingResolveClaims(float64(typed))
	case float64:
		return collectionListFloatingResolveClaims(typed)
	}
	return collectionResolveClaimsPlan{comparisonErr: transactionListPaginationTypeError(value)}
}

func collectionListSignedResolveClaims(value int64) collectionResolveClaimsPlan {
	if value <= 0 {
		return collectionResolveClaimsPlan{}
	}
	if uint64(value) > uint64(math.MaxInt) {
		return collectionResolveClaimsPlan{enabled: true, limit: math.MaxInt}
	}
	return collectionResolveClaimsPlan{enabled: true, limit: int(value)}
}

func collectionListUnsignedResolveClaims(value uint64) collectionResolveClaimsPlan {
	if value == 0 {
		return collectionResolveClaimsPlan{}
	}
	if value > uint64(math.MaxInt) {
		return collectionResolveClaimsPlan{enabled: true, limit: math.MaxInt}
	}
	return collectionResolveClaimsPlan{enabled: true, limit: int(value)}
}

func collectionListPositiveBigInt(value *big.Int) int {
	maximum := new(big.Int).SetUint64(uint64(math.MaxInt))
	if value.Cmp(maximum) > 0 {
		return math.MaxInt
	}
	return int(value.Int64())
}

func collectionListFloatingResolveClaims(value float64) collectionResolveClaimsPlan {
	if value <= 0 || math.IsNaN(value) {
		return collectionResolveClaimsPlan{}
	}
	return collectionResolveClaimsPlan{
		enabled: true,
		itemErr: transactionListApplicationError{
			name:    "TypeError",
			message: "slice indices must be integers or None or have an __index__ method",
		},
	}
}
