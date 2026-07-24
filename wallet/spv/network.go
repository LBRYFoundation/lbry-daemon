package spv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	SDKVersion                 = "0.113.0"
	DefaultClientName          = "LBRY SDK " + SDKVersion
	DefaultProtocolMinimum     = "0.65.0"
	DefaultProtocolMaximum     = SDKVersion
	DefaultReconnectDelay      = 30 * time.Second
	DefaultVersionTimeout      = 3 * time.Second
	DefaultKeepaliveTimeout    = 3 * time.Second
	DefaultKeepaliveIdleTime   = 60 * time.Second
	orderedTransactionBatch    = "blockchain.transaction.get_batch"
	TransactionBroadcastMethod = "blockchain.transaction.broadcast"
)

var (
	ErrNetworkStopped      = errors.New("SPV network is stopped")
	ErrNetworkStopping     = errors.New("SPV network is still stopping")
	ErrNoServers           = errors.New("no SPV servers are configured")
	ErrNoAvailableServers  = errors.New("no SPV server candidates are available")
	ErrIncompatibleServer  = errors.New("incompatible SPV server")
	ErrUnexpectedRPCResult = errors.New("unexpected SPV RPC result")
	ErrInvalidNetwork      = errors.New("invalid SPV network configuration")
)

// AddressSubscriptionCanceledError mirrors the pinned SDK's CancelledError
// when an address subscription times out. The timeout aborts the entire
// session because silently losing even one subscription is not recoverable.
type AddressSubscriptionCanceledError struct {
	Server Server
	Cause  error
}

func (err *AddressSubscriptionCanceledError) Error() string {
	if err == nil {
		return context.Canceled.Error()
	}
	if err.Server.Host == "" {
		return fmt.Sprintf("SPV address subscription canceled: %v", err.Cause)
	}
	return fmt.Sprintf("SPV address subscription to %s canceled: %v", err.Server, err.Cause)
}

func (err *AddressSubscriptionCanceledError) Unwrap() []error {
	if err == nil || err.Cause == nil {
		return []error{context.Canceled}
	}
	return []error{context.Canceled, err.Cause}
}

type NetworkConfig struct {
	Servers          []Server
	ExplicitServers  []Server
	KnownHubs        *KnownHubs
	Jurisdiction     *string
	Selector         CandidateSelector
	Client           ClientConfig
	ReconnectDelay   time.Duration
	VersionTimeout   time.Duration
	KeepaliveTimeout time.Duration
	KeepaliveIdle    time.Duration
	ClientName       string
	ProtocolMinimum  string
	ProtocolMaximum  string
}

func (config NetworkConfig) normalized() (NetworkConfig, error) {
	client, err := config.Client.normalized()
	if err != nil {
		return NetworkConfig{}, err
	}
	config.Client = client
	config.Servers = append([]Server(nil), config.Servers...)
	config.ExplicitServers = append([]Server(nil), config.ExplicitServers...)
	if config.Jurisdiction != nil {
		jurisdiction := *config.Jurisdiction
		config.Jurisdiction = &jurisdiction
	}
	if config.Selector == nil {
		config.Selector = SequentialSelector{}
	}
	if config.ReconnectDelay == 0 {
		config.ReconnectDelay = DefaultReconnectDelay
	}
	if config.VersionTimeout == 0 {
		config.VersionTimeout = DefaultVersionTimeout
	}
	if config.KeepaliveTimeout == 0 {
		config.KeepaliveTimeout = DefaultKeepaliveTimeout
	}
	if config.KeepaliveIdle == 0 {
		config.KeepaliveIdle = DefaultKeepaliveIdleTime
	}
	if config.ClientName == "" {
		config.ClientName = DefaultClientName
	}
	if config.ProtocolMinimum == "" {
		config.ProtocolMinimum = DefaultProtocolMinimum
	}
	if config.ProtocolMaximum == "" {
		config.ProtocolMaximum = DefaultProtocolMaximum
	}
	if config.ReconnectDelay < 0 || config.VersionTimeout < 0 ||
		config.KeepaliveTimeout < 0 || config.KeepaliveIdle < 0 {
		return NetworkConfig{}, ErrInvalidNetwork
	}
	if _, err := parseProtocolVersion(config.ProtocolMinimum); err != nil {
		return NetworkConfig{}, fmt.Errorf("%w: protocol minimum: %v", ErrInvalidNetwork, err)
	}
	return config, nil
}

type IncompatibleServerError struct {
	Server  Server
	Version string
	Minimum string
}

func (err *IncompatibleServerError) Error() string {
	if err == nil {
		return ErrIncompatibleServer.Error()
	}
	return fmt.Sprintf("SPV server %s protocol %s is below required %s", err.Server, err.Version, err.Minimum)
}

func (err *IncompatibleServerError) Unwrap() error { return ErrIncompatibleServer }

type Snapshot struct {
	Running      bool
	Connected    bool
	Server       Server
	RemoteHeight int
	Features     any
	LastError    error
}

type Network struct {
	config NetworkConfig

	mu                         sync.RWMutex
	running                    bool
	client                     *Client
	connecting                 *Client
	remoteHeight               int
	features                   any
	lastErr                    error
	headerHandler              func(context.Context, any)
	lastHeaderNotification     any
	hasLastHeaderNotification  bool
	addressHandler             func(context.Context, any)
	lastAddressNotification    any
	hasLastAddressNotification bool
	connectedHandler           func(context.Context)
	stateChanged               chan struct{}
	cancel                     context.CancelFunc
	done                       chan struct{}
	wake                       chan struct{}
}

func NewNetwork(config NetworkConfig) (*Network, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &Network{
		config:       normalized,
		stateChanged: make(chan struct{}),
		wake:         make(chan struct{}, 1),
	}, nil
}

func (network *Network) Start(ctx context.Context) error {
	if network == nil {
		return errors.New("SPV network is nil")
	}
	if ctx == nil {
		return errors.New("SPV network context is nil")
	}
	network.mu.Lock()
	if network.running {
		network.mu.Unlock()
		return nil
	}
	if network.done != nil {
		select {
		case <-network.done:
		default:
			network.mu.Unlock()
			return ErrNetworkStopping
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	network.running = true
	network.cancel = cancel
	network.done = done
	network.lastErr = nil
	network.signalStateLocked()
	network.mu.Unlock()
	go network.run(runCtx, done)
	return nil
}

func (network *Network) Stop(ctx context.Context) error {
	if network == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("SPV stop context is nil")
	}
	network.mu.Lock()
	done := network.done
	if !network.running {
		network.mu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	network.running = false
	cancel := network.cancel
	client := network.client
	connecting := network.connecting
	network.client = nil
	network.connecting = nil
	network.features = nil
	network.signalStateLocked()
	network.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if client != nil {
		_ = client.Close()
	}
	if connecting != nil && connecting != client {
		_ = connecting.Close()
	}
	network.wakeReconnect()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (network *Network) RequestReconnect() {
	if network == nil {
		return
	}
	network.mu.RLock()
	client := network.client
	connecting := network.connecting
	network.mu.RUnlock()
	if client != nil {
		_ = client.Close()
	}
	if connecting != nil && connecting != client {
		_ = connecting.Close()
	}
	network.wakeReconnect()
}

// SetHeaderNotificationHandler binds the ledger's serialized tip worker to
// blockchain.headers.subscribe notifications. It is safe before Start and
// while reconnecting; replacing the handler does not alter the active session.
func (network *Network) SetHeaderNotificationHandler(handler func(context.Context, any)) {
	if network == nil {
		return
	}
	network.mu.Lock()
	network.headerHandler = handler
	network.mu.Unlock()
}

// SetAddressNotificationHandler binds address status notifications to the
// ledger. Exact consecutive payloads are merged like Python's status stream.
func (network *Network) SetAddressNotificationHandler(handler func(context.Context, any)) {
	if network == nil {
		return
	}
	network.mu.Lock()
	network.addressHandler = handler
	network.mu.Unlock()
}

// SetConnectedHandler installs the callback used to restore subscriptions
// after every successful connection and reconnection.
func (network *Network) SetConnectedHandler(handler func(context.Context)) {
	if network == nil {
		return
	}
	network.mu.Lock()
	network.connectedHandler = handler
	network.mu.Unlock()
}

func (network *Network) IsConnected() bool {
	if network == nil {
		return false
	}
	network.mu.RLock()
	defer network.mu.RUnlock()
	return network.running && network.client != nil && network.client.IsConnected()
}

func (network *Network) RemoteHeight() int {
	if network == nil {
		return 0
	}
	network.mu.RLock()
	defer network.mu.RUnlock()
	return network.remoteHeight
}

func (network *Network) Snapshot() Snapshot {
	if network == nil {
		return Snapshot{}
	}
	network.mu.RLock()
	defer network.mu.RUnlock()
	snapshot := Snapshot{
		Running:      network.running,
		Connected:    network.running && network.client != nil && network.client.IsConnected(),
		RemoteHeight: network.remoteHeight,
		Features:     network.features,
		LastError:    network.lastErr,
	}
	if network.client != nil {
		snapshot.Server = network.client.Server()
	}
	return snapshot
}

func (network *Network) KnownHubCount() int {
	if network == nil || network.config.KnownHubs == nil {
		return 0
	}
	return network.config.KnownHubs.Len()
}

// RetriableValue mirrors Network.retriable_call without imposing a result
// shape. The restricted flag is intentionally inert because the pinned SDK
// has only one active session.
func (network *Network) RetriableValue(
	ctx context.Context, method string, params []any, restricted bool,
) (any, error) {
	_ = restricted
	if network == nil {
		return nil, ErrNetworkStopped
	}
	if ctx == nil {
		return nil, errors.New("SPV retriable-call context is nil")
	}
	for {
		client, err := network.waitForClient(ctx)
		if err != nil {
			return nil, err
		}
		var result any
		if method == orderedTransactionBatch {
			result, err = client.CallOrderedObject(ctx, method, params)
		} else {
			result, err = client.Call(ctx, method, params)
		}
		if err == nil {
			return result, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if errors.Is(err, ErrRequestTimeout) {
			continue
		}
		if errors.Is(err, ErrConnection) {
			network.detachClient(client, err)
			_ = client.Close()
			network.wakeReconnect()
			continue
		}
		return nil, err
	}
}

// OneShotValue sends one request through the currently published client. It
// never waits for a client or retries on a replacement session. A disconnected
// client still wakes the reconnect loop before the connection error is returned.
func (network *Network) OneShotValue(
	ctx context.Context, method string, params []any, restricted bool,
) (any, error) {
	return network.oneShotValue(ctx, method, restricted, func(client *Client) (any, error) {
		return client.Call(ctx, method, params)
	})
}

// OneShotNamedValue is OneShotValue for RPC methods whose params are a JSON
// object. It deliberately has no retriable counterpart: claim queries must not
// be replayed against a replacement session after a connection failure.
func (network *Network) OneShotNamedValue(
	ctx context.Context, method string, params map[string]any, restricted bool,
) (any, error) {
	return network.oneShotValue(ctx, method, restricted, func(client *Client) (any, error) {
		return client.CallNamed(ctx, method, params)
	})
}

func (network *Network) oneShotValue(
	ctx context.Context, method string, restricted bool,
	call func(*Client) (any, error),
) (any, error) {
	_ = restricted
	if network == nil {
		return nil, ErrNetworkStopped
	}
	if ctx == nil {
		return nil, errors.New("SPV one-shot-call context is nil")
	}
	network.mu.RLock()
	running := network.running
	client := network.client
	network.mu.RUnlock()
	if !running {
		return nil, ErrNetworkStopped
	}
	if client == nil || !client.IsConnected() {
		if client != nil {
			network.detachClient(client, client.Err())
			_ = client.Close()
		}
		network.wakeReconnect()
		return nil, &ConnectionError{
			Operation: "one-shot request " + method,
			Err:       ErrClientClosed,
		}
	}
	result, err := call(client)
	if err != nil && errors.Is(err, ErrConnection) {
		network.detachClient(client, err)
		_ = client.Close()
		network.wakeReconnect()
	}
	return result, err
}

// RetriableCall retains the mapping contract used by header and claim RPCs.
func (network *Network) RetriableCall(
	ctx context.Context, method string, params []any, restricted bool,
) (map[string]any, error) {
	result, err := network.RetriableValue(ctx, method, params, restricted)
	if err != nil {
		return nil, err
	}
	mapping, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: %s returned %T", ErrUnexpectedRPCResult, method, result)
	}
	return mapping, nil
}

// SubscribeAddresses subscribes the active master session directly. It does
// not retry on a different session: reconnecting is what guarantees the full
// address set will be subscribed again by the ledger's connected callback.
func (network *Network) SubscribeAddresses(ctx context.Context, addresses []string) ([]any, error) {
	if network == nil {
		return nil, ErrNetworkStopped
	}
	if ctx == nil {
		return nil, errors.New("SPV address-subscription context is nil")
	}
	network.mu.RLock()
	running := network.running
	client := network.client
	network.mu.RUnlock()
	if !running {
		return nil, ErrNetworkStopped
	}
	if client == nil || !client.IsConnected() {
		network.wakeReconnect()
		return nil, &ConnectionError{
			Operation: "address subscription",
			Err:       ErrClientClosed,
		}
	}
	params := make([]any, len(addresses))
	for index, address := range addresses {
		params[index] = address
	}
	result, err := client.Call(ctx, "blockchain.address.subscribe", params)
	if err != nil {
		if errors.Is(err, ErrRequestTimeout) {
			network.detachClient(client, err)
			_ = client.Close()
			network.wakeReconnect()
			return nil, &AddressSubscriptionCanceledError{Server: client.Server(), Cause: err}
		}
		if errors.Is(err, ErrConnection) {
			network.detachClient(client, err)
			_ = client.Close()
			network.wakeReconnect()
		}
		return nil, err
	}
	statuses, ok := result.([]any)
	if !ok {
		return nil, fmt.Errorf(
			"%w: blockchain.address.subscribe returned %T", ErrUnexpectedRPCResult, result,
		)
	}
	return statuses, nil
}

// BroadcastTransaction sends one request through the active master session.
// Retrying a submission could hide whether the hub accepted the transaction,
// so a connection failure is returned while reconnection proceeds in the
// background.
func (network *Network) BroadcastTransaction(
	ctx context.Context, rawTransaction string,
) (any, error) {
	if network == nil {
		return nil, ErrNetworkStopped
	}
	if ctx == nil {
		return nil, errors.New("SPV transaction-broadcast context is nil")
	}
	network.mu.RLock()
	running := network.running
	client := network.client
	network.mu.RUnlock()
	if !running {
		return nil, ErrNetworkStopped
	}
	if client == nil || !client.IsConnected() {
		network.wakeReconnect()
		return nil, &ConnectionError{
			Operation: "transaction broadcast",
			Err:       ErrClientClosed,
		}
	}
	result, err := client.Call(ctx, TransactionBroadcastMethod, []any{rawTransaction})
	if err != nil && errors.Is(err, ErrConnection) {
		network.detachClient(client, err)
		_ = client.Close()
		network.wakeReconnect()
	}
	return result, err
}

func (network *Network) run(ctx context.Context, done chan struct{}) {
	defer network.finishRun(done)
	immediate := true
	for {
		if !immediate {
			timer := time.NewTimer(network.config.ReconnectDelay)
			select {
			case <-ctx.Done():
				stopTimer(timer)
				return
			case <-network.wake:
				stopTimer(timer)
			case <-timer.C:
			}
		}
		immediate = false
		if err := ctx.Err(); err != nil {
			return
		}
		client, features, height, err := network.connect(ctx)
		if err != nil {
			network.recordError(err)
			continue
		}
		if !network.publishClient(ctx, client, features, height) {
			_ = client.Close()
			return
		}

		sessionCtx, cancelSession := context.WithCancel(ctx)
		keepaliveDone := make(chan error, 1)
		go func() { keepaliveDone <- network.keepalive(sessionCtx, client) }()
		urgent := false
		keepaliveConsumed := false
		var sessionErr error
		select {
		case <-ctx.Done():
			sessionErr = ctx.Err()
		case <-client.Done():
			sessionErr = client.Err()
		case <-network.wake:
			urgent = true
		case sessionErr = <-keepaliveDone:
			keepaliveConsumed = true
		}
		cancelSession()
		_ = client.Close()
		if !keepaliveConsumed {
			<-keepaliveDone
		}
		network.detachClient(client, sessionErr)
		if ctx.Err() != nil {
			return
		}
		immediate = urgent
	}
}

func (network *Network) connect(ctx context.Context) (*Client, any, int, error) {
	servers := network.selectionServers()
	if len(servers) == 0 {
		return nil, nil, 0, ErrNoServers
	}
	candidates, err := network.config.Selector.Select(ctx, SelectionRequest{
		Servers:      servers,
		KnownHubs:    network.config.KnownHubs,
		Jurisdiction: network.config.Jurisdiction,
	})
	if err != nil {
		return nil, nil, 0, err
	}
	if len(candidates) == 0 {
		return nil, nil, 0, ErrNoAvailableServers
	}
	var lastErr error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		if network.config.Jurisdiction != nil && candidate.Pong != nil {
			country, countryErr := candidate.Pong.CountryName()
			if countryErr != nil || country != *network.config.Jurisdiction {
				continue
			}
		}
		server := candidate.Server
		client, err := Dial(ctx, server, network.config.Client)
		if err != nil {
			lastErr = err
			continue
		}
		network.setConnectingClient(client)
		client.SetNotificationHandler(func(notificationCtx context.Context, method string, params any) {
			network.handleNotification(client, notificationCtx, method, params)
		})
		features, height, err := network.handshake(ctx, client)
		if err == nil {
			return client, features, height, nil
		}
		network.clearConnectingClient(client)
		lastErr = err
		_ = client.Close()
	}
	if lastErr == nil {
		lastErr = ErrNoAvailableServers
	}
	return nil, nil, 0, lastErr
}

func (network *Network) selectionServers() []Server {
	if len(network.config.ExplicitServers) > 0 {
		return append([]Server(nil), network.config.ExplicitServers...)
	}
	if network.config.KnownHubs != nil && network.config.KnownHubs.Len() > 0 {
		return network.config.KnownHubs.Servers()
	}
	return append([]Server(nil), network.config.Servers...)
}

func (network *Network) handshake(ctx context.Context, client *Client) (any, int, error) {
	versionCtx, cancelVersion := context.WithTimeout(ctx, network.config.VersionTimeout)
	versionResponse, err := client.Call(versionCtx, "server.version", []any{
		network.config.ClientName, network.config.ProtocolMaximum,
	})
	cancelVersion()
	if err != nil {
		return nil, 0, err
	}
	version, err := serverProtocolVersion(versionResponse)
	if err != nil {
		return nil, 0, err
	}
	compatible, err := protocolAtLeast(version, network.config.ProtocolMinimum)
	if err != nil {
		return nil, 0, err
	}
	if !compatible {
		return nil, 0, &IncompatibleServerError{
			Server: client.Server(), Version: version, Minimum: network.config.ProtocolMinimum,
		}
	}
	features, err := client.Call(ctx, "server.features", []any{})
	if err != nil {
		return nil, 0, err
	}
	peers, err := client.Call(ctx, "server.peers.subscribe", []any{})
	if err != nil {
		return nil, 0, err
	}
	network.updateKnownHubs(peers)
	header, err := client.Call(ctx, "blockchain.headers.subscribe", []any{true})
	if err != nil {
		return nil, 0, err
	}
	height, err := headerHeight(header)
	if err != nil {
		return nil, 0, err
	}
	return features, height, nil
}

func (network *Network) keepalive(ctx context.Context, client *Client) error {
	for {
		lastSend, lastReceive := client.activity()
		now := time.Now()
		oldest := lastSend
		if lastReceive.Before(oldest) {
			oldest = lastReceive
		}
		if oldest.Add(network.config.KeepaliveIdle).Before(now) {
			pingCtx, cancel := context.WithTimeout(ctx, network.config.KeepaliveTimeout)
			_, err := client.Call(pingCtx, "server.ping", []any{})
			cancel()
			if err != nil {
				return err
			}
			continue
		}
		delay := network.config.KeepaliveIdle - now.Sub(lastSend)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return ctx.Err()
		case <-client.Done():
			stopTimer(timer)
			return client.Err()
		case <-timer.C:
		}
	}
}

func (network *Network) waitForClient(ctx context.Context) (*Client, error) {
	for {
		network.mu.RLock()
		if !network.running {
			network.mu.RUnlock()
			return nil, ErrNetworkStopped
		}
		client := network.client
		changed := network.stateChanged
		network.mu.RUnlock()
		if client != nil && client.IsConnected() {
			return client, nil
		}
		network.wakeReconnect()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (network *Network) publishClient(ctx context.Context, client *Client, features any, height int) bool {
	network.mu.Lock()
	if !network.running || ctx.Err() != nil {
		network.mu.Unlock()
		return false
	}
	network.client = client
	network.connecting = nil
	network.features = features
	network.remoteHeight = height
	network.lastErr = nil
	network.signalStateLocked()
	connectedHandler := network.connectedHandler
	network.mu.Unlock()
	// Clear wakeups raised by waiters while disconnected before the connected
	// callback can raise a new, meaningful reconnect request.
	network.drainReconnectWake()
	if connectedHandler != nil {
		connectedHandler(ctx)
	}
	return true
}

func (network *Network) detachClient(client *Client, err error) {
	network.mu.Lock()
	defer network.mu.Unlock()
	if network.client != client {
		return
	}
	network.client = nil
	network.features = nil
	if err != nil && !errors.Is(err, context.Canceled) {
		network.lastErr = err
	}
	network.signalStateLocked()
}

func (network *Network) recordError(err error) {
	network.mu.Lock()
	network.lastErr = err
	network.mu.Unlock()
}

func (network *Network) handleNotification(
	client *Client, ctx context.Context, method string, params any,
) {
	network.mu.RLock()
	activeClient := network.running && (network.client == client || network.connecting == client)
	network.mu.RUnlock()
	if !activeClient {
		return
	}
	if method == "blockchain.headers.subscribe" {
		height, heightErr := notificationHeight(params)
		network.mu.Lock()
		activeClient = network.running && (network.client == client || network.connecting == client)
		duplicate := activeClient && network.hasLastHeaderNotification &&
			reflect.DeepEqual(network.lastHeaderNotification, params)
		if activeClient && !duplicate {
			network.lastHeaderNotification = cloneHubValue(params)
			network.hasLastHeaderNotification = true
		}
		if activeClient && !duplicate && heightErr == nil {
			network.remoteHeight = height
			network.signalStateLocked()
		}
		headerHandler := network.headerHandler
		network.mu.Unlock()
		if activeClient && !duplicate && headerHandler != nil {
			headerHandler(ctx, params)
		}
	}
	if method == "blockchain.address.subscribe" {
		network.mu.Lock()
		activeClient = network.running && (network.client == client || network.connecting == client)
		duplicate := activeClient && network.hasLastAddressNotification &&
			reflect.DeepEqual(network.lastAddressNotification, params)
		if activeClient && !duplicate {
			network.lastAddressNotification = cloneHubValue(params)
			network.hasLastAddressNotification = true
		}
		addressHandler := network.addressHandler
		network.mu.Unlock()
		if activeClient && !duplicate && addressHandler != nil {
			addressHandler(ctx, params)
		}
	}
	if method == "blockchain.peers.subscribe" {
		network.updateKnownHubs(params)
	}
	if configured := network.config.Client.NotificationHandler; configured != nil {
		configured(ctx, method, params)
	}
}

func (network *Network) updateKnownHubs(value any) {
	known := network.config.KnownHubs
	if known == nil || value == nil {
		return
	}
	var hubs []any
	switch typed := value.(type) {
	case []any:
		hubs = typed
	case []string:
		hubs = make([]any, len(typed))
		for index, hub := range typed {
			hubs[index] = hub
		}
	default:
		return
	}
	added := false
	for _, rawHub := range hubs {
		hub, ok := rawHub.(string)
		if !ok {
			if pythonFalseyHub(rawHub) {
				continue
			}
			return
		}
		inserted, err := known.SetString(hub, HubDetails{})
		if err != nil {
			return
		}
		added = added || inserted
	}
	if added {
		_ = known.Save()
	}
}

func pythonFalseyHub(value any) bool {
	if value == nil {
		return true
	}
	if number, ok := value.(json.Number); ok {
		parsed, err := strconv.ParseFloat(number.String(), 64)
		return err == nil && parsed == 0
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Bool:
		return !reflected.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return reflected.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() == 0
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return reflected.Len() == 0
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer:
		return reflected.IsNil()
	default:
		return false
	}
}

func (network *Network) finishRun(done chan struct{}) {
	network.mu.Lock()
	client := network.client
	connecting := network.connecting
	network.client = nil
	network.connecting = nil
	network.features = nil
	network.running = false
	network.cancel = nil
	network.signalStateLocked()
	close(done)
	network.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if connecting != nil && connecting != client {
		_ = connecting.Close()
	}
}

func (network *Network) setConnectingClient(client *Client) {
	network.mu.Lock()
	if network.running {
		network.connecting = client
	}
	network.mu.Unlock()
}

func (network *Network) clearConnectingClient(client *Client) {
	network.mu.Lock()
	if network.connecting == client {
		network.connecting = nil
	}
	network.mu.Unlock()
}

func (network *Network) signalStateLocked() {
	close(network.stateChanged)
	network.stateChanged = make(chan struct{})
}

func (network *Network) wakeReconnect() {
	select {
	case network.wake <- struct{}{}:
	default:
	}
}

func (network *Network) drainReconnectWake() {
	select {
	case <-network.wake:
	default:
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil || !timer.Stop() {
		return
	}
}

func serverProtocolVersion(response any) (string, error) {
	values, ok := response.([]any)
	if !ok || len(values) < 2 {
		return "", fmt.Errorf("%w: server.version returned %T", ErrUnexpectedRPCResult, response)
	}
	version, ok := values[1].(string)
	if !ok {
		return "", fmt.Errorf("%w: server.version protocol has type %T", ErrUnexpectedRPCResult, values[1])
	}
	return version, nil
}

func headerHeight(response any) (int, error) {
	header, ok := response.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("%w: header subscription returned %T", ErrUnexpectedRPCResult, response)
	}
	value, exists := header["height"]
	if !exists {
		return 0, fmt.Errorf("%w: header subscription is missing height", ErrUnexpectedRPCResult)
	}
	return integerHeight(value)
}

func notificationHeight(params any) (int, error) {
	values, ok := params.([]any)
	if !ok || len(values) == 0 {
		return 0, fmt.Errorf("%w: header notification has type %T", ErrUnexpectedRPCResult, params)
	}
	return headerHeight(values[0])
}

func integerHeight(value any) (int, error) {
	var parsed int64
	switch height := value.(type) {
	case json.Number:
		integer, err := height.Int64()
		if err != nil {
			return 0, fmt.Errorf("%w: height %q is not an integer", ErrUnexpectedRPCResult, height)
		}
		parsed = integer
	case int:
		return height, nil
	case int64:
		parsed = height
	default:
		return 0, fmt.Errorf("%w: height has type %T", ErrUnexpectedRPCResult, value)
	}
	converted := int(parsed)
	if int64(converted) != parsed {
		return 0, fmt.Errorf("%w: height %d exceeds the Go integer range", ErrUnexpectedRPCResult, parsed)
	}
	return converted, nil
}

func protocolAtLeast(version, minimum string) (bool, error) {
	actual, err := parseProtocolVersion(version)
	if err != nil {
		return false, fmt.Errorf("%w: server protocol %q: %v", ErrUnexpectedRPCResult, version, err)
	}
	required, err := parseProtocolVersion(minimum)
	if err != nil {
		return false, err
	}
	length := max(len(actual), len(required))
	for index := 0; index < length; index++ {
		if index >= len(actual) {
			return false, nil
		}
		if index >= len(required) {
			return true, nil
		}
		if actual[index] != required[index] {
			return actual[index] > required[index], nil
		}
	}
	return true, nil
}

func parseProtocolVersion(version string) ([]int, error) {
	parts := strings.Split(version, ".")
	parsed := make([]int, len(parts))
	for index, part := range parts {
		if part == "" {
			return nil, errors.New("empty version component")
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, err
		}
		parsed[index] = value
	}
	return parsed, nil
}
