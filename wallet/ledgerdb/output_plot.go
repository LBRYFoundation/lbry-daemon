package ledgerdb

import (
	"context"
)

type OutputPlotRow struct {
	Day   string
	Total int64
}

func (database *DB) PlotOutputs(ctx context.Context, query OutputQuery) ([]OutputPlotRow, error) {
	if database == nil || database.sql == nil {
		return nil, ErrNotOpen
	}
	if ctx == nil {
		ctx = context.Background()
	}
	statement, arguments, err := buildOutputQuery(
		"DATE(tx.day) AS day, SUM(txo.amount) AS total", query, true,
	)
	if err != nil {
		return nil, err
	}
	statement += " GROUP BY day ORDER BY day"
	rows, err := database.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]OutputPlotRow, 0)
	for rows.Next() {
		var row OutputPlotRow
		if err := rows.Scan(&row.Day, &row.Total); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
