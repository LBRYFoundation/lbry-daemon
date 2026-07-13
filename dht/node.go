package dht

import "bytes"
import "context"
import "crypto/rand"
import "crypto/sha512"
import "encoding/binary"
import "encoding/hex"
import "errors"
import "fmt"
import "log"
import "net"
import "strconv"
import "sort"
import "sync"
import "time"

// LBRY Kademlia DHT constants.
const (
	HashSize               = 48 // SHA-384 = 48 bytes
	K                      = 8  // k-bucket size
	Alpha                  = 5  // parallel lookups
	RPCIDSize              = 20
	RPCTimeout             = 5 * time.Second
	MsgSizeLimit           = 1400
	TokenSize              = HashSize
	ProtocolVersion        = 1
	CompactAddrSize        = 4 + 2 + HashSize // IPv4 + port + node_id = 54
	AnnouncementExpiration = 24 * time.Hour
	RefreshInterval        = time.Hour
)

const METHOD_PING = "ping"
const METHOD_FIND_NODE = "findNode"
const METHOD_FIND_VALUE = "findValue"
const METHOD_STORE = "store"

const TYPE_REQUEST = 0
const TYPE_RESPONSE = 1
const TYPE_ERROR = 2

// Bootstrap seed nodes.
var SeedNodes = []string{
	"51.83.238.186:4444",
	"dht.lbry.grin.io:4444",
	"dht.lizard.technology:4444",
	"lbrynet1.lbry.com:4444",
	"lbrynet2.lbry.com:4444",
	"lbrynet3.lbry.com:4444",
	"lbrynet4.lbry.com:4444",
	"s1.lbry.network:4444",
	"s2.lbry.network:4444",
}

// Peer holds a contact in the network.
type Peer struct {
	ID       [HashSize]byte
	IP       net.IP
	UDPPort  int
	TCPPort  int // for blob exchange
	LastSeen time.Time
}

// CompactAddr encodes a peer for find_value responses: 4 bytes IP + 2 bytes TCP port + 48 bytes node_id.
func (p *Peer) CompactAddr() []byte {
	b := make([]byte, CompactAddrSize)
	copy(b[0:4], p.IP.To4())
	binary.BigEndian.PutUint16(b[4:6], uint16(p.TCPPort))
	copy(b[6:], p.ID[:])
	return b
}

// ParseCompactAddr decodes a compact address.
func ParseCompactAddr(b []byte) (ip net.IP, tcpPort int, nodeID [HashSize]byte) {
	ip = net.IPv4(b[0], b[1], b[2], b[3])
	tcpPort = int(binary.BigEndian.Uint16(b[4:6]))
	copy(nodeID[:], b[6:6+HashSize])
	return
}

// XOR distance between two IDs.
func Distance(a, b [HashSize]byte) [HashSize]byte {
	var d [HashSize]byte
	for i := range d {
		d[i] = a[i] ^ b[i]
	}
	return d
}

// Less returns true if distance a < b (big-endian comparison).
func DistanceLess(a, b [HashSize]byte) bool {
	for i := range a {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return false
}

// --- Routing Table ---

type KBucket struct {
	peers []Peer
}

type RoutingTable struct {
	selfID   [HashSize]byte
	buckets  [HashSize * 8]KBucket // 384 buckets
	failures map[[HashSize]byte]int
	mu       sync.RWMutex
}

func NewRoutingTable(selfID [HashSize]byte) *RoutingTable {
	return &RoutingTable{selfID: selfID, failures: make(map[[HashSize]byte]int)}
}

// bucketIndex returns which bucket a node belongs in (prefix length of XOR).
func (rt *RoutingTable) bucketIndex(nodeID [HashSize]byte) int {
	dist := Distance(rt.selfID, nodeID)
	for i := 0; i < HashSize; i++ {
		for bit := 7; bit >= 0; bit-- {
			if dist[i]&(1<<uint(bit)) != 0 {
				return i*8 + (7 - bit)
			}
		}
	}
	return HashSize*8 - 1
}

func (rt *RoutingTable) AddPeer(p Peer) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if p.ID == rt.selfID || p.IP.To4() == nil || p.UDPPort <= 0 || p.UDPPort > 65535 {
		return false
	}
	for bucketIndex := range rt.buckets {
		bucket := &rt.buckets[bucketIndex]
		for peerIndex := 0; peerIndex < len(bucket.peers); peerIndex++ {
			existing := bucket.peers[peerIndex]
			if existing.ID != p.ID && existing.UDPPort == p.UDPPort && existing.IP.Equal(p.IP) {
				delete(rt.failures, existing.ID)
				bucket.peers = append(bucket.peers[:peerIndex], bucket.peers[peerIndex+1:]...)
				peerIndex--
			}
		}
	}
	idx := rt.bucketIndex(p.ID)
	bucket := &rt.buckets[idx]

	// Existing contacts move to the end, preserving the Kademlia LRU order.
	for i, existing := range bucket.peers {
		if existing.ID == p.ID {
			updated := existing
			updated.LastSeen = time.Now()
			updated.IP = append(net.IP(nil), p.IP...)
			updated.UDPPort = p.UDPPort
			if p.TCPPort > 0 {
				updated.TCPPort = p.TCPPort
			}
			bucket.peers = append(bucket.peers[:i], bucket.peers[i+1:]...)
			bucket.peers = append(bucket.peers, updated)
			delete(rt.failures, p.ID)
			return true
		}
	}

	if len(bucket.peers) < K {
		p.LastSeen = time.Now()
		p.IP = append(net.IP(nil), p.IP...)
		bucket.peers = append(bucket.peers, p)
		delete(rt.failures, p.ID)
		return true
	}
	return false
}

func (rt *RoutingTable) RemovePeer(nodeID [HashSize]byte) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	idx := rt.bucketIndex(nodeID)
	bucket := &rt.buckets[idx]
	for index, peer := range bucket.peers {
		if peer.ID == nodeID {
			bucket.peers = append(bucket.peers[:index], bucket.peers[index+1:]...)
			delete(rt.failures, nodeID)
			return true
		}
	}
	delete(rt.failures, nodeID)
	return false
}

// MarkFailure removes a contact after two consecutive failed RPCs, matching
// the pinned SDK's distinction between questionable and bad contacts.
func (rt *RoutingTable) MarkFailure(nodeID [HashSize]byte) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.failures[nodeID]++
	if rt.failures[nodeID] < 2 {
		return false
	}
	idx := rt.bucketIndex(nodeID)
	bucket := &rt.buckets[idx]
	for index, peer := range bucket.peers {
		if peer.ID == nodeID {
			bucket.peers = append(bucket.peers[:index], bucket.peers[index+1:]...)
			delete(rt.failures, nodeID)
			return true
		}
	}
	delete(rt.failures, nodeID)
	return false
}

// PeerCount returns the number of unique peers currently held by the table.
func (rt *RoutingTable) PeerCount() int {
	if rt == nil {
		return 0
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	count := 0
	for index := range rt.buckets {
		count += len(rt.buckets[index].peers)
	}
	return count
}

func (rt *RoutingTable) BucketsSnapshot() map[int][]Peer {
	result := make(map[int][]Peer)
	if rt == nil {
		return result
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for index, bucket := range rt.buckets {
		result[index] = append([]Peer(nil), bucket.peers...)
	}
	return result
}

func (n *Node) RoutingBucketsSnapshot() map[int][]Peer {
	if n == nil {
		return map[int][]Peer{}
	}
	return n.routing.BucketsSnapshot()
}

// ClosestPeers returns up to n peers closest to key.
func (rt *RoutingTable) ClosestPeers(key [HashSize]byte, n int) []Peer {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	type peerDist struct {
		peer Peer
		dist [HashSize]byte
	}
	var all []peerDist
	for _, bucket := range rt.buckets {
		for _, p := range bucket.peers {
			all = append(all, peerDist{p, Distance(key, p.ID)})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return DistanceLess(all[i].dist, all[j].dist)
	})
	if len(all) > n {
		all = all[:n]
	}
	result := make([]Peer, len(all))
	for i, pd := range all {
		result[i] = pd.peer
	}
	return result
}

// --- DHT Node ---

type Node struct {
	ID                [HashSize]byte
	UDPPort           int
	TCPPort           int // blob exchange port
	BindIP            net.IP
	BootstrapNodes    []string
	conn              *net.UDPConn
	routing           *RoutingTable
	pending           map[string]pendingRPC // rpcID -> expected response
	announcements     map[[HashSize]byte]map[[HashSize]byte]Peer
	announcementTimes map[[HashSize]byte]map[[HashSize]byte]time.Time
	announcementOrder [][HashSize]byte
	mu                sync.RWMutex
	running           bool
	started           bool
	ctx               context.Context
	cancel            context.CancelFunc
	loopWG            sync.WaitGroup
	tokenSecret       [HashSize]byte // random secret for token generation
	now               func() time.Time
	bootstrapInterval time.Duration
	bootstrapFn       func() error
}

type pendingRPC struct {
	response chan map[string]any
	address  *net.UDPAddr
}

func NewNode(udpPort int) (*Node, error) {
	var id [HashSize]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("dht: generate node ID: %w", err)
	}
	return NewNodeWithID(udpPort, id)
}

// NewNodeWithID constructs a DHT node with a caller-supplied persistent ID.
func NewNodeWithID(udpPort int, id [HashSize]byte) (*Node, error) {
	var secret [HashSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, fmt.Errorf("dht: generate token secret: %w", err)
	}

	node := &Node{
		ID:                id,
		UDPPort:           udpPort,
		BootstrapNodes:    append([]string(nil), SeedNodes...),
		routing:           NewRoutingTable(id),
		pending:           make(map[string]pendingRPC),
		announcements:     make(map[[HashSize]byte]map[[HashSize]byte]Peer),
		announcementTimes: make(map[[HashSize]byte]map[[HashSize]byte]time.Time),
		tokenSecret:       secret,
		now:               time.Now,
		bootstrapInterval: 30 * time.Second,
	}
	return node, nil
}

// NodeIDHex returns the immutable node identity in the legacy lowercase form.
func (n *Node) NodeIDHex() string {
	if n == nil {
		return ""
	}
	return hex.EncodeToString(n.ID[:])
}

// NodeID returns the immutable binary node identity.
func (n *Node) NodeID() [HashSize]byte {
	if n == nil {
		return [HashSize]byte{}
	}
	return n.ID
}

// StoredBlobHashes returns a caller-owned snapshot of incoming announcement
// keys in the order this node first received them.
func (n *Node) StoredBlobHashes() [][HashSize]byte {
	if n == nil {
		return nil
	}
	n.mu.RLock()
	hashes := make([][HashSize]byte, 0, len(n.announcementOrder))
	for _, hash := range n.announcementOrder {
		if len(n.announcements[hash]) > 0 {
			hashes = append(hashes, hash)
		}
	}
	n.mu.RUnlock()
	return hashes
}

// RoutingPeerCount returns a point-in-time routing-table peer count.
func (n *Node) RoutingPeerCount() int {
	if n == nil {
		return 0
	}
	return n.routing.PeerCount()
}

// ObservePeer records a validated contact learned by a protocol or discovery
// adapter while keeping routing-table synchronization inside Node.
func (n *Node) ObservePeer(peer Peer) {
	if n == nil {
		return
	}
	n.routing.AddPeer(peer)
}

func (n *Node) Start() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started {
		return errors.New("dht: node is already running")
	}
	addr := &net.UDPAddr{IP: append(net.IP(nil), n.BindIP...), Port: n.UDPPort}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("dht: listen UDP: %w", err)
	}
	n.conn = conn
	n.running = true
	n.started = true
	n.ctx, n.cancel = context.WithCancel(context.Background())
	n.UDPPort = conn.LocalAddr().(*net.UDPAddr).Port

	n.loopWG.Add(3)
	go n.bootstrapLoop(n.ctx)
	go n.readLoop(n.ctx, conn)
	go n.refreshLoop(n.ctx)

	log.Printf("DHT node started on UDP port %d (ID: %s...)", n.UDPPort, hex.EncodeToString(n.ID[:8]))
	return nil
}

func (n *Node) bootstrapLoop(ctx context.Context) {
	defer n.loopWG.Done()
	for {
		if n.routing.PeerCount() == 0 {
			bootstrap := n.Bootstrap
			if n.bootstrapFn != nil {
				bootstrap = n.bootstrapFn
			}
			err := bootstrap()
			if ctx.Err() != nil {
				return
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("DHT: bootstrap failed: %v; retrying in %s", err, n.bootstrapInterval)
			}
		}
		timer := time.NewTimer(n.bootstrapInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// refreshLoop periodically refreshes the routing table.
func (n *Node) refreshLoop(ctx context.Context) {
	defer n.loopWG.Done()
	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Println("DHT: refreshing routing table...")
			n.expireAnnouncements(n.now())
			// Lookup a random ID to discover new peers.
			var randomID [HashSize]byte
			_, _ = rand.Read(randomID[:])
			n.FindNode(randomID)
			n.FindNode(n.ID)
		}
	}
}

func (n *Node) Stop() {
	n.mu.Lock()
	if !n.running {
		n.mu.Unlock()
		return
	}
	n.running = false
	cancel := n.cancel
	conn := n.conn
	n.mu.Unlock()

	cancel()
	_ = conn.Close()
	n.loopWG.Wait()
}

// readLoop handles incoming UDP messages.
func (n *Node) readLoop(ctx context.Context, conn *net.UDPConn) {
	defer n.loopWG.Done()
	buf := make([]byte, MsgSizeLimit+100)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
			return
		}
		nBytes, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			continue
		}
		packet := append([]byte(nil), buf[:nBytes]...)
		n.handleMessage(packet, remoteAddr)
	}
}

func (n *Node) handleMessage(data []byte, from *net.UDPAddr) {
	if from == nil || len(data) == 0 || len(data) > MsgSizeLimit {
		return
	}
	decoded, consumed, err := BencodeDecode(data)
	if err != nil || consumed != len(data) {
		return
	}
	msg, ok := decoded.(map[string]any)
	if !ok {
		return
	}
	messageType, typeOK := msg["0"].(int64)
	rpcID, rpcOK := msg["1"].([]byte)
	nodeID, nodeOK := msg["2"].([]byte)
	if !typeOK || messageType < TYPE_REQUEST || messageType > TYPE_ERROR ||
		!rpcOK || len(rpcID) != RPCIDSize || !nodeOK || len(nodeID) != HashSize {
		return
	}

	dhtMessage := DHTMessage{
		Type:      messageType,
		RPCID:     rpcID,
		NodeID:    nodeID,
		Arguments: getBencList(msg, "4"),
	}

	if dhtMessage.Type == TYPE_REQUEST {
		method, methodOK := msg["3"].([]byte)
		if !methodOK || len(method) == 0 {
			return
		}
		dhtMessage.Payload = method
	}

	switch dhtMessage.Type {
	case TYPE_REQUEST:
		n.handleRequest(from, dhtMessage)
	case TYPE_RESPONSE, TYPE_ERROR:
		key := string(dhtMessage.RPCID)
		n.mu.RLock()
		pending, ok := n.pending[key]
		n.mu.RUnlock()
		if ok && sameUDPAddress(pending.address, from) {
			var id [HashSize]byte
			copy(id[:], dhtMessage.NodeID)
			n.routing.AddPeer(Peer{ID: id, IP: from.IP, UDPPort: from.Port})
			select {
			case pending.response <- msg:
			default:
			}
		}
	}
}

type DHTMessage struct {
	Type      int64
	RPCID     []byte
	NodeID    []byte
	Payload   any
	Arguments []any
}

type MethodFunction func(*net.UDPAddr, []any) (any, []any, error)

func (n *Node) handleRequest(from *net.UDPAddr, message DHTMessage) {
	method, ok := message.Payload.([]byte)
	if !ok {
		return
	}
	function, ok := (map[string]MethodFunction{
		METHOD_PING:       n.handlePing,
		METHOD_FIND_NODE:  n.handleFindNode,
		METHOD_FIND_VALUE: n.handleFindValue,
		METHOD_STORE:      n.handleStore,
	})[string(method)]

	if ok {
		requestArguments := authenticatedRequestArguments(message)
		payload, arguments, err := function(from, requestArguments)
		if err != nil {
			n.sendMessage(from, DHTMessage{
				Type:    TYPE_ERROR,
				RPCID:   message.RPCID,
				Payload: []byte(err.Error()),
			})
			return
		}
		n.sendMessage(from, DHTMessage{
			Type:      TYPE_RESPONSE,
			RPCID:     message.RPCID,
			Payload:   payload,
			Arguments: arguments,
		})
		return
	}

	n.sendMessage(from, DHTMessage{
		Type:    TYPE_ERROR,
		RPCID:   message.RPCID,
		Payload: []byte("Unknown method"),
	})
}

func authenticatedRequestArguments(message DHTMessage) []any {
	method, ok := message.Payload.([]byte)
	if !ok || string(method) != METHOD_STORE || len(message.NodeID) != HashSize || len(message.Arguments) < 4 {
		return message.Arguments
	}
	arguments := append([]any(nil), message.Arguments...)
	arguments[3] = append([]byte(nil), message.NodeID...)
	return arguments
}

func (n *Node) handlePing(from *net.UDPAddr, args []any) (any, []any, error) {
	return "pong", nil, nil
}

func (n *Node) handleFindNode(from *net.UDPAddr, args []any) (any, []any, error) {
	if len(args) == 0 {
		return nil, nil, errors.New("Needs arguments")
	}
	key := toBytes(args[0])
	if len(key) != HashSize {
		return nil, nil, errors.New("Key fails length")
	}
	var keyArr [HashSize]byte
	copy(keyArr[:], key)
	peers := n.routing.ClosestPeers(keyArr, K)
	contacts := peersToContactList(peers)

	return contacts, nil, nil
}

func (n *Node) handleFindValue(from *net.UDPAddr, args []any) (any, []any, error) {
	if len(args) == 0 {
		return nil, nil, errors.New("Needs arguments")
	}
	key := toBytes(args[0])
	if len(key) != HashSize {
		return nil, nil, errors.New("Key fails length")
	}
	var keyArr [HashSize]byte
	copy(keyArr[:], key)
	page := int64(0)
	if keywordArguments := getKeywordArguments(args); keywordArguments != nil {
		page = toBencInt(keywordArguments["p"])
		if page < 0 {
			page = 0
		}
	}
	token := n.makeToken(from.IP)
	resp := map[string]any{
		"token":           string(token),
		"protocolVersion": ProtocolVersion,
	}
	if page == 0 {
		resp["contacts"] = peersToContactList(n.routing.ClosestPeers(keyArr, K))
	}
	n.mu.RLock()
	stored := n.announcements[keyArr]
	timestamps := n.announcementTimes[keyArr]
	announced := make([][]byte, 0, len(stored))
	now := n.now()
	for publisherID, peer := range stored {
		if now.Before(timestamps[publisherID].Add(AnnouncementExpiration)) {
			announced = append(announced, peer.CompactAddr())
		}
	}
	n.mu.RUnlock()
	// Map iteration cannot define page boundaries. A stable ordering prevents
	// providers from being skipped or repeated between page requests.
	sort.Slice(announced, func(left, right int) bool {
		return bytes.Compare(announced[left], announced[right]) < 0
	})
	pages := int64(0)
	if len(announced) > 0 {
		pages = int64(len(announced)/(K+1) + 1)
	}
	resp["p"] = pages
	if page <= int64(len(announced)/K) {
		start := int(page) * K
		if start < len(announced) {
			end := min(start+K, len(announced))
			providers := make([]any, end-start)
			for index := start; index < end; index++ {
				providers[index-start] = announced[index]
			}
			resp[string(key)] = providers
		}
	}

	return resp, nil, nil
}

func (n *Node) handleStore(from *net.UDPAddr, args []any) (any, []any, error) {
	if len(args) < 5 {
		return nil, nil, errors.New("store needs five arguments")
	}
	blobHash, token := toBytes(args[0]), toBytes(args[1])
	port, publisher := toBencInt(args[2]), toBytes(args[3])
	if len(blobHash) != HashSize || len(token) != TokenSize || len(publisher) != HashSize {
		return nil, nil, errors.New("invalid store arguments")
	}
	if port <= 0 || port > 65535 {
		return nil, nil, errors.New("invalid tcp port")
	}
	if !n.verifyToken(token, from.IP) {
		return nil, nil, errors.New("Invalid token")
	}
	var key, publisherID [HashSize]byte
	copy(key[:], blobHash)
	copy(publisherID[:], publisher)
	peer := Peer{ID: publisherID, IP: append(net.IP(nil), from.IP...), UDPPort: from.Port, TCPPort: int(port)}
	n.mu.Lock()
	if n.announcements[key] == nil {
		n.announcements[key] = make(map[[HashSize]byte]Peer)
		n.announcementTimes[key] = make(map[[HashSize]byte]time.Time)
		n.announcementOrder = append(n.announcementOrder, key)
	}
	n.announcements[key][publisherID] = peer
	n.announcementTimes[key][publisherID] = n.now()
	n.mu.Unlock()
	return "OK", nil, nil
}

func (n *Node) expireAnnouncements(now time.Time) {
	if n == nil {
		return
	}
	n.mu.Lock()
	activeOrder := make([][HashSize]byte, 0, len(n.announcementOrder))
	for _, hash := range n.announcementOrder {
		providers := n.announcements[hash]
		timestamps := n.announcementTimes[hash]
		for publisherID := range providers {
			if timestamps[publisherID].Add(AnnouncementExpiration).Before(now) {
				delete(providers, publisherID)
				delete(timestamps, publisherID)
			}
		}
		if len(providers) == 0 {
			delete(n.announcements, hash)
			delete(n.announcementTimes, hash)
			continue
		}
		activeOrder = append(activeOrder, hash)
	}
	n.announcementOrder = activeOrder
	n.mu.Unlock()
}

func (n *Node) makeToken(ip net.IP) []byte {
	h := sha512.New384()
	h.Write(n.tokenSecret[:])
	h.Write(ip.To4())
	return h.Sum(nil)
}

func (n *Node) verifyToken(token []byte, ip net.IP) bool {
	expected := n.makeToken(ip)
	if len(token) != len(expected) {
		return false
	}
	var mismatch byte
	for index := range token {
		mismatch |= token[index] ^ expected[index]
	}
	return mismatch == 0
}

func (n *Node) sendMessage(to *net.UDPAddr, message DHTMessage) error {
	msg := map[int]any{
		0: message.Type,
		1: message.RPCID,
		2: n.ID[:],
		3: message.Payload,
	}
	if message.Arguments != nil {
		msg[4] = message.Arguments
	}

	data, err := BencodeEncode(msg)
	if err != nil {
		return err
	}
	if len(data) > MsgSizeLimit {
		return fmt.Errorf("dht: datagram exceeds %d bytes", MsgSizeLimit)
	}
	n.mu.RLock()
	conn := n.conn
	running := n.running
	n.mu.RUnlock()
	if running && conn != nil {
		_, err = conn.WriteToUDP(data, to)
		return err
	}
	return errors.New("dht: node stopped")
}

// sendRPC sends a request and waits for response.
func (n *Node) sendRPC(to *net.UDPAddr, method string, args []any) (map[string]any, error) {
	n.mu.RLock()
	ctx := n.ctx
	running := n.running
	n.mu.RUnlock()
	if !running || ctx == nil {
		return nil, errors.New("dht: node is not running")
	}

	rpcID := make([]byte, RPCIDSize)
	_, _ = rand.Read(rpcID)

	// Add protocolVersion to last arg if it's a dict, or create one
	pvDict := map[string]any{"protocolVersion": 1}
	if len(args) > 0 {
		if d, ok := args[len(args)-1].(map[string]any); ok {
			d["protocolVersion"] = 1
		} else {
			args = append(args, pvDict)
		}
	} else {
		args = []any{pvDict}
	}

	ch := make(chan map[string]any, 1)
	key := string(rpcID)
	n.mu.Lock()
	n.pending[key] = pendingRPC{
		response: ch,
		address:  &net.UDPAddr{IP: append(net.IP(nil), to.IP...), Port: to.Port, Zone: to.Zone},
	}
	n.mu.Unlock()

	defer func() {
		n.mu.Lock()
		delete(n.pending, key)
		n.mu.Unlock()
	}()

	if err := n.sendMessage(to, DHTMessage{
		Type:      TYPE_REQUEST,
		RPCID:     rpcID,
		Payload:   []byte(method),
		Arguments: args,
	}); err != nil {
		return nil, err
	}

	timer := time.NewTimer(RPCTimeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if getBencInt(resp, "0") == TYPE_ERROR {
			return nil, errors.New(string(getBencBytes(resp, "3")))
		}
		return resp, nil
	case <-timer.C:
		return nil, fmt.Errorf("rpc timeout")
	case <-ctx.Done():
		return nil, errors.New("dht: node stopped")
	}
}

// Ping sends a ping to a peer.
func (n *Node) Ping(addr *net.UDPAddr) error {
	_, err := n.sendRPC(addr, METHOD_PING, nil)
	return err
}

// FindNode performs an iterative find_node lookup.
func (n *Node) FindNode(key [HashSize]byte) []Peer {
	return n.iterativeLookup(key, false)
}

// FindValue performs an iterative find_value lookup, returning blob peers.
// Returns (blob_peers, closest_nodes).
func (n *Node) FindValue(key [HashSize]byte) ([]Peer, []Peer) {
	providerByAddress := make(map[string]Peer)
	providerOrder := make([]string, 0)
	var mu sync.Mutex

	collect := func(resp map[string]any) int {
		peers := extractBlobPeers(resp, key)
		mu.Lock()
		defer mu.Unlock()
		for _, peer := range peers {
			address := net.JoinHostPort(peer.IP.String(), strconv.Itoa(peer.TCPPort))
			if _, exists := providerByAddress[address]; !exists {
				providerOrder = append(providerOrder, address)
			}
			providerByAddress[address] = peer
		}
		return len(peers)
	}

	closest := n.iterativeLookupWithCallback(key, true, func(peer Peer, resp map[string]any) {
		count := collect(resp)
		payload, _ := getBencAny(resp, "3").(map[string]any)
		lastPage := toBencInt(payload["p"])
		for page := int64(1); count >= K && page <= lastPage; page++ {
			pageResponse, err := n.sendRPC(&net.UDPAddr{IP: peer.IP, Port: peer.UDPPort}, METHOD_FIND_VALUE,
				[]any{key[:], map[string]any{"p": page}})
			if err != nil {
				break
			}
			count = collect(pageResponse)
		}
	})

	blobPeers := make([]Peer, 0, len(providerOrder))
	for _, address := range providerOrder {
		blobPeers = append(blobPeers, providerByAddress[address])
	}
	return blobPeers, closest
}

// AnnounceBlob publishes this node's blob TCP endpoint to the closest DHT peers.
func (n *Node) AnnounceBlob(blobHash string) ([][HashSize]byte, error) {
	raw, err := hex.DecodeString(blobHash)
	if err != nil || len(raw) != HashSize {
		return nil, errors.New("dht: invalid blob hash")
	}
	if n.TCPPort <= 0 || n.TCPPort > 65535 {
		return nil, errors.New("dht: invalid blob tcp port")
	}
	var key [HashSize]byte
	copy(key[:], raw)
	peers := n.FindNode(key)
	type result struct {
		id  [HashSize]byte
		err error
	}
	results := make(chan result, len(peers))
	for _, peer := range peers {
		go func(peer Peer) {
			address := &net.UDPAddr{IP: peer.IP, Port: peer.UDPPort}
			response, callErr := n.sendRPC(address, METHOD_FIND_VALUE, []any{key[:], map[string]any{"p": 0}})
			if callErr == nil {
				payload, ok := getBencAny(response, "3").(map[string]any)
				if !ok || len(toBytes(payload["token"])) != TokenSize {
					callErr = errors.New("dht: findValue response has no valid token")
				} else {
					_, callErr = n.sendRPC(address, METHOD_STORE, []any{
						key[:], toBytes(payload["token"]), n.TCPPort, n.ID[:], 0,
					})
				}
			}
			results <- result{id: peer.ID, err: callErr}
		}(peer)
	}
	stored := make([][HashSize]byte, 0, len(peers))
	for range peers {
		result := <-results
		if result.err == nil {
			stored = append(stored, result.id)
		}
	}
	return stored, nil
}

// iterativeLookup performs the Kademlia iterative lookup.
func (n *Node) iterativeLookup(key [HashSize]byte, findValue bool) []Peer {
	return n.iterativeLookupWithCallback(key, findValue, nil)
}

func (n *Node) iterativeLookupWithCallback(key [HashSize]byte, findValue bool, onResponse func(Peer, map[string]any)) []Peer {
	shortlist := n.routing.ClosestPeers(key, K)
	if len(shortlist) == 0 {
		return nil
	}

	contacted := make(map[[HashSize]byte]bool)
	responded := make(map[[HashSize]byte]bool)
	type result struct {
		peer     Peer
		resp     map[string]any
		err      error
		newPeers []Peer
	}

	for round := 0; round < 10; round++ {
		// Pick up to Alpha uncontacted peers from shortlist
		var toQuery []Peer
		for _, p := range shortlist {
			if !contacted[p.ID] && len(toQuery) < Alpha {
				toQuery = append(toQuery, p)
			}
		}
		if len(toQuery) == 0 {
			break
		}

		results := make(chan result, len(toQuery))
		for _, p := range toQuery {
			contacted[p.ID] = true
			go func(peer Peer) {
				addr := &net.UDPAddr{IP: peer.IP, Port: peer.UDPPort}
				var resp map[string]any
				var err error
				method := METHOD_FIND_NODE
				args := []any{key[:]}
				if findValue {
					method = METHOD_FIND_VALUE
					args = []any{key[:], map[string]any{"p": 0}}
				}
				resp, err = n.sendRPC(addr, method, args)

				var newPeers []Peer
				if err == nil {
					newPeers = extractContacts(resp)
				}
				results <- result{peer, resp, err, newPeers}
			}(p)
		}

		for i := 0; i < len(toQuery); i++ {
			r := <-results
			if r.err != nil {
				n.routing.MarkFailure(r.peer.ID)
				continue
			}
			responded[r.peer.ID] = true
			if onResponse != nil {
				onResponse(r.peer, r.resp)
			}
			// Add new peers to shortlist
			for _, np := range r.newPeers {
				found := false
				for _, sp := range shortlist {
					if sp.ID == np.ID {
						found = true
						break
					}
				}
				if !found && np.ID != n.ID {
					shortlist = append(shortlist, np)
				}
			}
		}
		// A failed incumbent may have occupied a full bucket when a successful
		// newcomer replied. Retry verified contacts after failure pruning.
		for _, peer := range shortlist {
			if responded[peer.ID] {
				n.routing.AddPeer(peer)
			}
		}

		// Sort shortlist by distance to key
		sort.Slice(shortlist, func(i, j int) bool {
			di := Distance(key, shortlist[i].ID)
			dj := Distance(key, shortlist[j].ID)
			return DistanceLess(di, dj)
		})
		if len(shortlist) > K*2 {
			shortlist = shortlist[:K*2]
		}
	}

	answering := make([]Peer, 0, K)
	for _, peer := range shortlist {
		if responded[peer.ID] {
			answering = append(answering, peer)
			if len(answering) == K {
				break
			}
		}
	}
	return answering
}

// Bootstrap connects to seed nodes and populates the routing table.
func (n *Node) Bootstrap() error {
	log.Println("DHT: bootstrapping...")
	pinged := 0
	for _, seed := range n.BootstrapNodes {
		n.mu.RLock()
		running := n.running
		ctx := n.ctx
		n.mu.RUnlock()
		if !running {
			return errors.New("dht: node stopped")
		}
		addr, err := resolveUDPAddr(ctx, seed)
		if err != nil {
			continue
		}
		if err := n.Ping(addr); err == nil {
			pinged++
			log.Printf("DHT: connected to seed %s", seed)
			if pinged >= 3 {
				break // enough seeds
			}
		}
	}
	if pinged == 0 {
		return fmt.Errorf("dht: failed to contact any seed nodes")
	}

	// Lookup own ID to discover nearby peers
	n.FindNode(n.ID)
	log.Printf("DHT: bootstrap complete, routing table has peers")
	return nil
}

func resolveUDPAddr(ctx context.Context, address string) (*net.UDPAddr, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		return &net.UDPAddr{IP: ip, Port: port}, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, resolved := range addresses {
		if ip := resolved.IP.To4(); ip != nil {
			return &net.UDPAddr{IP: ip, Port: port}, nil
		}
	}
	return nil, fmt.Errorf("dht: no IPv4 address for %s", host)
}

// --- Helpers ---

func sameUDPAddress(expected, actual *net.UDPAddr) bool {
	return expected != nil && actual != nil && expected.Port == actual.Port &&
		expected.Zone == actual.Zone && expected.IP.Equal(actual.IP)
}

func peersToContactList(peers []Peer) []any {
	var list []any
	for _, p := range peers {
		ip4 := p.IP.To4()
		if ip4 == nil {
			continue
		}
		contact := []any{p.ID[:], []byte(ip4), p.UDPPort}
		list = append(list, contact)
	}
	return list
}

func extractContacts(resp map[string]any) []Peer {
	var peers []Peer
	val := getBencAny(resp, "3")

	// Response can be a list (findNode) or dict (findValue)
	switch v := val.(type) {
	case []any:
		peers = parseContactTriples(v)
	case map[string]any:
		if contacts, ok := v["contacts"]; ok {
			if cl, ok := contacts.([]any); ok {
				peers = parseContactTriples(cl)
			}
		}
	}
	return peers
}

func parseContactTriples(list []any) []Peer {
	var peers []Peer
	for _, item := range list {
		triple, ok := item.([]any)
		if !ok || len(triple) < 3 {
			continue
		}
		nodeIDBytes := toBytes(triple[0])
		ipBytes := toBytes(triple[1])
		port := toBencInt(triple[2])

		if len(nodeIDBytes) != HashSize || port <= 0 || port > 65535 {
			continue
		}

		var id [HashSize]byte
		copy(id[:], nodeIDBytes)

		// IP can be 4 raw bytes OR a string like "51.83.238.186"
		var ip net.IP
		if len(ipBytes) == 4 {
			ip = net.IPv4(ipBytes[0], ipBytes[1], ipBytes[2], ipBytes[3])
		} else {
			ip = net.ParseIP(string(ipBytes))
		}
		if ip == nil {
			continue
		}

		peers = append(peers, Peer{
			ID:      id,
			IP:      ip,
			UDPPort: int(port),
		})
	}
	return peers
}

func extractBlobPeers(response map[string]any, key [HashSize]byte) []Peer {
	payload, ok := getBencAny(response, "3").(map[string]any)
	if !ok {
		return nil
	}
	encoded, ok := payload[string(key[:])].([]any)
	if !ok {
		return nil
	}
	peers := make([]Peer, 0, len(encoded))
	for _, item := range encoded {
		compact := toBytes(item)
		if len(compact) != CompactAddrSize {
			continue
		}
		ip, port, nodeID := ParseCompactAddr(compact)
		if ip.To4() == nil || port <= 0 || port > 65535 {
			continue
		}
		peers = append(peers, Peer{ID: nodeID, IP: ip, TCPPort: port})
	}
	return peers
}

func getBencInt(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	return toBencInt(v)
}

func toBencInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func getBencBytes(m map[string]any, key string) []byte {
	v, ok := m[key]
	if !ok {
		return nil
	}
	return toBytes(v)
}

func toBytes(v any) []byte {
	switch b := v.(type) {
	case []byte:
		return b
	case string:
		return []byte(b)
	}
	return nil
}

func getBencList(m map[string]any, key string) []any {
	v, ok := m[key]
	if !ok {
		return nil
	}
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func getBencAny(m map[string]any, key string) any {
	return m[key]
}

func getKeywordArguments(args []any) map[string]any {
	if len(args) > 0 {
		if keywordArguments, ok := args[len(args)-1].(map[string]any); ok {
			return keywordArguments
		}
	}
	return nil
}
