package wallet

// LegacyTransactionOutputJSONWithOptions exposes the encoder used for public
// output lists. Standalone outputs use the encoder's default signature check.
func (ledger *Ledger) LegacyTransactionOutputJSONWithOptions(
	output *TransactionOutput, options LegacyTransactionJSONOptions,
) (map[string]any, error) {
	return ledger.legacyTransactionOutputJSON(output, options, true)
}
