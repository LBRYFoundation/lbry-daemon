package wallet

import "context"

func CreateAbandonTransaction(
	ctx context.Context,
	outputs []*TransactionOutput,
	replacement []TransactionOutput,
	accounts []*Account,
	changeAccount *Account,
) (*Transaction, error) {
	inputs := make([]TransactionInput, len(outputs))
	for index, output := range outputs {
		input, err := NewSpendInput(output)
		if err != nil {
			return nil, err
		}
		inputs[index] = input
	}
	return CreateTransaction(ctx, inputs, replacement, accounts, changeAccount, true)
}
