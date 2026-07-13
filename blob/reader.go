package blob

import (
	"context"
	"errors"
	"fmt"
)

// WalkStream acquires, decrypts, and yields one data blob at a time in
// descriptor order. The callback must not retain data after it returns.
func WalkStream(
	ctx context.Context,
	manager *BlobManager,
	descriptor *StreamDescriptor,
	callback func(BlobInfo, []byte) error,
) error {
	if descriptor == nil {
		return errors.New("stream descriptor is nil")
	}
	return WalkStreamRange(ctx, manager, descriptor, 0, len(descriptor.Blobs)-1, callback)
}

// WalkStreamRange is WalkStream restricted to the half-open data-blob range
// [first, end). It keeps range streaming bounded to one decrypted blob.
func WalkStreamRange(
	ctx context.Context,
	manager *BlobManager,
	descriptor *StreamDescriptor,
	first int,
	end int,
	callback func(BlobInfo, []byte) error,
) error {
	if ctx == nil {
		return errors.New("stream walk context is nil")
	}
	if manager == nil || callback == nil {
		return errors.New("stream walk dependencies are unavailable")
	}
	if err := ValidateDescriptor(descriptor); err != nil {
		return err
	}
	contentCount := len(descriptor.Blobs) - 1
	if first < 0 || end < first || end > contentCount {
		return fmt.Errorf("stream blob range [%d,%d) is outside [0,%d)", first, end, contentCount)
	}
	for _, info := range descriptor.Blobs[first:end] {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := manager.Ensure(ctx, info.BlobHash); err != nil {
			return fmt.Errorf("acquire stream blob %d: %w", info.BlobNum, err)
		}
		encrypted, ok := manager.GetLocal(info.BlobHash)
		if !ok {
			return fmt.Errorf("stream blob %s is unavailable", info.BlobHash)
		}
		decrypted, err := DecryptBlob(encrypted, descriptor.Key, info.IV)
		if err != nil {
			return fmt.Errorf("decrypt stream blob %d: %w", info.BlobNum, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := callback(info, decrypted); err != nil {
			return err
		}
	}
	return nil
}
