package blob

import (
	"context"
	"errors"
	"fmt"

	"lbry/daemon/dht"
)

func DHTFetcher(nodeProvider func() *dht.Node) Fetcher {
	return func(ctx context.Context, blobHash string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if nodeProvider == nil || nodeProvider() == nil {
			return nil, errors.New("DHT node is unavailable")
		}
		hash, err := hexToHash(blobHash)
		if err != nil {
			return nil, err
		}
		peers, _ := nodeProvider().FindValue(hash)
		if len(peers) == 0 {
			return nil, fmt.Errorf("no peers found for blob %s", blobHash[:12])
		}
		var lastErr error
		for _, peer := range peers {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if peer.IP == nil || peer.TCPPort <= 0 {
				continue
			}
			data, downloadErr := DownloadBlobContext(ctx, peer.IP, peer.TCPPort, blobHash)
			if downloadErr == nil {
				return data, nil
			}
			lastErr = downloadErr
		}
		if lastErr == nil {
			return nil, fmt.Errorf("no usable peers found for blob %s", blobHash[:12])
		}
		return nil, fmt.Errorf("all peers failed for blob %s: %w", blobHash[:12], lastErr)
	}
}
