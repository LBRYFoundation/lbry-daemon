package rpc

import (
	"errors"
	"fmt"
	"net/http"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleCollectionResolve(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	if _, err := rpcServer.selectedWallet(normalized); err != nil {
		panic(err)
	}
	claimID, _ := normalized.named["claim_id"].(string)
	url, _ := normalized.named["url"].(string)
	if claimID == "" && url == "" {
		panic(errors.New("Missing argument claim_id or url."))
	}
	page := supportSumInteger(normalized.named["page"], 1)
	pageSize := supportSumInteger(normalized.named["page_size"], 20)
	pageNumber := page
	if pageNumber < 0 {
		pageNumber = -pageNumber
	}
	if pageSize < 0 {
		pageSize = -pageSize
	}
	pageSize = min(pageSize, 50)
	if pageSize == 0 {
		panic(errors.New("integer division or modulo by zero"))
	}
	ledger := rpcServer.walletManagerProvider().DefaultLedger()
	items, total, err := ledger.ResolveCollectionReference(
		normalized.ctx, claimID, url, pageSize*(pageNumber-1), pageSize,
	)
	if err != nil {
		if errors.Is(err, walletpkg.ErrCollectionReferenceNotFound) {
			if claimID != "" {
				panic(fmt.Errorf("Could not find collection with claim_id '%s'.", claimID))
			}
			panic(fmt.Errorf("Could not find collection with url '%s'.", url))
		}
		panic(err)
	}
	encoded := make([]any, len(items))
	for index, item := range items {
		if item == nil {
			encoded[index] = nil
			continue
		}
		value, err := ledger.LegacyTransactionOutputJSONWithOptions(
			item, walletpkg.LegacyTransactionJSONOptions{},
		)
		if err != nil {
			panic(err)
		}
		encoded[index] = value
	}
	sendResultResponse(response, map[string]any{
		"items": encoded, "total_pages": (total + pageSize - 1) / pageSize,
		"total_items": total, "page_size": pageSize, "page": page,
	})
}
