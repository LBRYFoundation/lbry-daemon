package wallet

import (
	"context"
	"errors"
	"time"

	"lbry/daemon/wallet/ledgerdb"
)

func (ledger *Ledger) PlotTransactionOutputs(
	ctx context.Context, query ledgerdb.OutputQuery,
) ([]ledgerdb.OutputPlotRow, error) {
	if err := validateTransactionOutputQuery(ledger); err != nil {
		return nil, err
	}
	return ledger.Database.PlotOutputs(ctx, query)
}

func (ledger *Ledger) CurrentEstimatedJulianDay() (float64, error) {
	if ledger == nil || ledger.Headers == nil {
		return 0, errors.New("ledger headers are unavailable")
	}
	timestamp, ok := ledger.Headers.EstimatedTimestamp(ledger.Headers.Height(), false)
	if !ok {
		return 0, errors.New("current header timestamp is unavailable")
	}
	date := time.Unix(timestamp, 0).In(time.Local)
	return float64(gregorianOrdinal(date.Year(), int(date.Month()), date.Day())) + 1721424.5, nil
}
