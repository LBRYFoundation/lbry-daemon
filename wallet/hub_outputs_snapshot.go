package wallet

import (
	"context"
	"errors"
)

var ErrHubOutputsSnapshotEncoding = errors.New("hub outputs snapshot encoding failed")

// HubOutputsSnapshotEncodingError marks failures from the legacy response
// encoder after query, inflation, annotation, and response validation succeed.
type HubOutputsSnapshotEncodingError struct {
	Err error
}

func (err *HubOutputsSnapshotEncodingError) Error() string {
	if err == nil || err.Err == nil {
		return ""
	}
	return err.Err.Error()
}

func (err *HubOutputsSnapshotEncodingError) Unwrap() error {
	if err == nil || err.Err == nil {
		return ErrHubOutputsSnapshotEncoding
	}
	return errors.Join(ErrHubOutputsSnapshotEncoding, err.Err)
}

// HubOutputsSnapshot is detached from the shared transaction cache and can be
// encoded after the ledger's inflation serialization boundary is released.
type HubOutputsSnapshot struct {
	Items   []any
	Blocked map[string]any
	Offset  uint32
	Total   uint32
}

// SnapshotHubOutputs keeps shared relation inflation, wallet annotation, and
// recursive output encoding in one serialized phase. Python executes these
// mutations on one event-loop thread; the explicit boundary prevents Go RPC
// handlers from racing while they inspect cached channel or blocked outputs.
func (ledger *Ledger) SnapshotHubOutputs(
	ctx context.Context,
	outputs *HubOutputs,
	annotationOptions ResolvedTransactionOutputAnnotationOptions,
	wireOptions LegacyTransactionJSONOptions,
) (HubOutputsSnapshot, error) {
	return ledger.snapshotHubOutputs(ctx, outputs, annotationOptions, wireOptions, nil)
}

// SnapshotHubOutputsBeforeEncoding runs beforeEncoding after all wallet
// annotations but before recursive legacy encoding, while the shared inflation
// lock is still held.
func (ledger *Ledger) SnapshotHubOutputsBeforeEncoding(
	ctx context.Context,
	outputs *HubOutputs,
	annotationOptions ResolvedTransactionOutputAnnotationOptions,
	wireOptions LegacyTransactionJSONOptions,
	beforeEncoding func(HubOutputsPage) error,
) (HubOutputsSnapshot, error) {
	return ledger.snapshotHubOutputs(
		ctx, outputs, annotationOptions, wireOptions, beforeEncoding,
	)
}

func (ledger *Ledger) snapshotHubOutputs(
	ctx context.Context,
	outputs *HubOutputs,
	annotationOptions ResolvedTransactionOutputAnnotationOptions,
	wireOptions LegacyTransactionJSONOptions,
	beforeEncoding func(HubOutputsPage) error,
) (HubOutputsSnapshot, error) {
	var snapshot HubOutputsSnapshot
	err := ledger.processHubOutputs(ctx, outputs, func(page HubOutputsPage) error {
		primary := make([]*TransactionOutput, len(page.Items))
		for index, result := range page.Items {
			primary[index] = result.Output
		}
		enriched, err := ledger.EnrichResolvedTransactionOutputAnnotations(
			ctx, primary, annotationOptions,
		)
		if err != nil {
			return err
		}
		if beforeEncoding != nil {
			if err := beforeEncoding(page); err != nil {
				return err
			}
		}

		items := make([]any, len(page.Items))
		for index, result := range page.Items {
			if result.Output != nil {
				result.Output = enriched[index]
			}
			items[index], err = ledger.snapshotHubInflatedResult(result, wireOptions)
			if err != nil {
				return &HubOutputsSnapshotEncodingError{Err: err}
			}
		}

		blockedChannels := make([]any, len(page.Blocked.Channels))
		for index, channel := range page.Blocked.Channels {
			encoded, encodeErr := ledger.snapshotHubInflatedResult(
				channel.Channel, wireOptions,
			)
			if encodeErr != nil {
				return &HubOutputsSnapshotEncodingError{Err: encodeErr}
			}
			blockedChannels[index] = map[string]any{
				"channel": encoded,
				"blocked": channel.Blocked,
			}
		}
		snapshot = HubOutputsSnapshot{
			Items: items,
			Blocked: map[string]any{
				"total": page.Blocked.Total, "channels": blockedChannels,
			},
			Offset: page.Offset,
			Total:  page.Total,
		}
		return nil
	})
	return snapshot, err
}

func (ledger *Ledger) snapshotHubInflatedResult(
	result HubInflatedResult,
	options LegacyTransactionJSONOptions,
) (any, error) {
	if result.Output != nil {
		return ledger.LegacyTransactionOutputJSONWithOptions(result.Output, options)
	}
	if result.Error == nil {
		return nil, nil
	}
	errorValue := map[string]any{
		"name": result.Error.Name,
		"text": result.Error.Text,
	}
	if result.Error.Censor != nil {
		censor, err := ledger.snapshotHubInflatedResult(*result.Error.Censor, options)
		if err != nil {
			return nil, err
		}
		errorValue["censor"] = censor
	}
	return map[string]any{"error": errorValue}, nil
}
