package rpc

import (
	"errors"
	"net/http"
	"time"

	walletpkg "lbry/daemon/wallet"
)

var txoPlotParameterNames = map[string]struct{}{
	"account_id": {}, "wallet_id": {}, "days_back": {},
	"start_day": {}, "days_after": {}, "end_day": {},
}

func (rpcServer *RPCServer) handleTXOPlot(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager, selectedWallet, _, accountID := rpcServer.txoSelection(normalized, "get_txo_plot", false)
	query, err := txoOutputQuery(normalized, txoPlotParameterNames)
	if err != nil {
		panic(err)
	}
	accounts := selectedWallet.Accounts
	if accountID != nil {
		account, err := selectedWallet.Account(*accountID)
		if err != nil {
			panic(err)
		}
		accounts = []*walletpkg.Account{account}
	}
	query.AccountIDs = make([]string, len(accounts))
	for index, account := range accounts {
		query.AccountIDs[index] = account.ID
	}
	ledger := manager.DefaultLedger()
	startDay, _ := normalized.named["start_day"].(string)
	if startDay == "" {
		current, err := ledger.CurrentEstimatedJulianDay()
		if err != nil {
			panic(err)
		}
		daysBack := supportSumInteger(normalized.named["days_back"], 0)
		start := current - float64(daysBack)
		query.DayGTE = &start
	} else {
		start, err := txoPlotJulianDay(startDay)
		if err != nil {
			panic(err)
		}
		query.DayGTE = &start
		if endDay, ok := normalized.named["end_day"].(string); ok && endDay != "" {
			end, err := txoPlotJulianDay(endDay)
			if err != nil {
				panic(err)
			}
			query.DayLTE = &end
		} else if normalized.named["days_after"] != nil {
			end := start + float64(supportSumInteger(normalized.named["days_after"], 0))
			query.DayLTE = &end
		}
	}
	rows, err := ledger.PlotTransactionOutputs(normalized.ctx, query)
	if err != nil {
		panic(err)
	}
	result := make([]any, len(rows))
	for index, row := range rows {
		result[index] = map[string]any{"day": row.Day, "total": dewiesString(row.Total)}
	}
	sendResultResponse(response, result)
}

func txoPlotJulianDay(value string) (float64, error) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return 0, err
	}
	if date.Year() < 1 {
		return 0, errors.New("year is out of range")
	}
	return float64(txoPlotGregorianOrdinal(date.Year(), int(date.Month()), date.Day())) + 1721424.5, nil
}

func txoPlotGregorianOrdinal(year, month, day int) int64 {
	priorYears := int64(year - 1)
	ordinal := 365*priorYears + priorYears/4 - priorYears/100 + priorYears/400
	offsets := [...]int64{0, 0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	ordinal += offsets[month] + int64(day)
	if month > 2 && year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		ordinal++
	}
	return ordinal
}
