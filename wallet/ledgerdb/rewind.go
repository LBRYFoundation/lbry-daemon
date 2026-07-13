package ledgerdb

import "context"

// RewindBlockchain preserves the pinned SDK's current placeholder behavior.
// Python records the call after finding a replacement chain but does not yet
// delete transactions or rewrite address histories.
func (database *DB) RewindBlockchain(context.Context, int) error {
	return nil
}
