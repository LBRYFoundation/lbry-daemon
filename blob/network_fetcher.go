package blob

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"lbry/daemon/dht"
)

type NetworkFetcherOptions struct {
	NodeProvider   func() *dht.Node
	FixedPeers     []string
	FixedPeerDelay time.Duration
	TrackerServers []string
	AnnouncePort   int
}

type discoveredPeer struct {
	ip   net.IP
	port int
}

type peerBatch struct {
	peers []discoveredPeer
	err   error
}

type downloadResult struct {
	data []byte
	err  error
}

// NetworkFetcher mirrors the SDK downloader's DHT/tracker-first and delayed
// fixed-peer discovery order while racing available blob-exchange peers.
func NetworkFetcher(options NetworkFetcherOptions) Fetcher {
	return func(ctx context.Context, blobHash string) ([]byte, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		fetchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		hash, err := hexToHash(blobHash)
		if err != nil {
			return nil, err
		}

		batches := make(chan peerBatch, 3)
		sources := 0
		var releaseFixedPeers chan struct{}
		var releaseFixedPeersOnce sync.Once
		if len(options.FixedPeers) > 0 {
			releaseFixedPeers = make(chan struct{})
			if options.NodeProvider == nil {
				close(releaseFixedPeers)
			}
		}
		if options.NodeProvider != nil {
			sources++
			go discoverDHTPeersWithFallback(options.NodeProvider, hash, batches, func() {
				if releaseFixedPeers != nil {
					releaseFixedPeersOnce.Do(func() { close(releaseFixedPeers) })
				}
			})
		}
		if len(options.TrackerServers) > 0 {
			sources++
			go discoverTrackerPeers(fetchCtx, options.TrackerServers, hash, options.AnnouncePort, batches)
		}
		if len(options.FixedPeers) > 0 {
			sources++
			go discoverFixedPeersWithFallback(fetchCtx, options.FixedPeers, options.FixedPeerDelay, releaseFixedPeers, batches)
		}
		if sources == 0 {
			return nil, errors.New("no blob peer sources are configured")
		}

		results := make(chan downloadResult)
		seen := make(map[string]struct{})
		active := 0
		finishedSources := 0
		var lastErr error
		for finishedSources < sources || active > 0 {
			select {
			case <-fetchCtx.Done():
				return nil, fetchCtx.Err()
			case batch := <-batches:
				finishedSources++
				if batch.err != nil {
					lastErr = batch.err
				}
				for _, peer := range batch.peers {
					if peer.ip == nil || peer.port <= 0 {
						continue
					}
					address := net.JoinHostPort(peer.ip.String(), strconv.Itoa(peer.port))
					if _, exists := seen[address]; exists {
						continue
					}
					seen[address] = struct{}{}
					active++
					go func(peer discoveredPeer) {
						data, downloadErr := DownloadBlobContext(fetchCtx, peer.ip, peer.port, blobHash)
						select {
						case results <- downloadResult{data: data, err: downloadErr}:
						case <-fetchCtx.Done():
						}
					}(peer)
				}
			case result := <-results:
				active--
				if result.err == nil {
					return result.data, nil
				}
				lastErr = result.err
			}
		}
		if lastErr == nil {
			lastErr = errors.New("no usable peers found")
		}
		return nil, fmt.Errorf("all peer sources failed for blob %s: %w", blobHash[:12], lastErr)
	}
}

func discoverDHTPeers(provider func() *dht.Node, hash [48]byte, output chan<- peerBatch) {
	discoverDHTPeersWithFallback(provider, hash, output, nil)
}

func discoverDHTPeersWithFallback(provider func() *dht.Node, hash [48]byte, output chan<- peerBatch, noPeers func()) {
	node := provider()
	if node == nil {
		if noPeers != nil {
			noPeers()
		}
		output <- peerBatch{err: errors.New("DHT node is unavailable")}
		return
	}
	if node.RoutingPeerCount() == 0 && noPeers != nil {
		noPeers()
	}
	peers, _ := node.FindValue(hash)
	result := make([]discoveredPeer, 0, len(peers))
	for _, peer := range peers {
		result = append(result, discoveredPeer{ip: peer.IP, port: peer.TCPPort})
	}
	output <- peerBatch{peers: result}
}

func discoverFixedPeers(ctx context.Context, addresses []string, delay time.Duration, output chan<- peerBatch) {
	discoverFixedPeersWithFallback(ctx, addresses, delay, nil, output)
}

func discoverFixedPeersWithFallback(ctx context.Context, addresses []string, delay time.Duration, release <-chan struct{}, output chan<- peerBatch) {
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			output <- peerBatch{err: ctx.Err()}
			return
		case <-release:
		case <-timer.C:
		}
	}
	peers, err := resolvePeerAddresses(ctx, addresses)
	output <- peerBatch{peers: peers, err: err}
}

func resolvePeerAddresses(ctx context.Context, addresses []string) ([]discoveredPeer, error) {
	var peers []discoveredPeer
	var failures []error
	for _, address := range addresses {
		host, portText, err := net.SplitHostPort(address)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			failures = append(failures, fmt.Errorf("invalid peer port in %q", address))
			continue
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, ip := range ips {
			peers = append(peers, discoveredPeer{ip: ip, port: port})
		}
	}
	return peers, errors.Join(failures...)
}

func discoverTrackerPeers(ctx context.Context, servers []string, hash [48]byte, announcePort int, output chan<- peerBatch) {
	var waitGroup sync.WaitGroup
	responses := make(chan peerBatch, len(servers))
	for _, server := range servers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			peers, err := queryUDPTracker(ctx, server, hash, announcePort)
			responses <- peerBatch{peers: peers, err: err}
		}()
	}
	waitGroup.Wait()
	close(responses)
	var peers []discoveredPeer
	var failures []error
	for response := range responses {
		peers = append(peers, response.peers...)
		if response.err != nil {
			failures = append(failures, response.err)
		}
	}
	output <- peerBatch{peers: peers, err: errors.Join(failures...)}
}
