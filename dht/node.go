package dht

import "crypto/rand"
import "crypto/sha512"
import "encoding/binary"
import "encoding/hex"
import "fmt"
import "log"
import "net"
import "sort"
import "sync"
import "time"

// LBRY Kademlia DHT constants.
const (
	HashSize        = 48 // SHA-384 = 48 bytes
	K               = 8  // k-bucket size
	Alpha           = 5  // parallel lookups
	RPCIDSize       = 20
	RPCTimeout      = 5 * time.Second
	MsgSizeLimit    = 1400
	TokenSize       = HashSize
	ProtocolVersion = 1
	CompactAddrSize = 4 + 2 + HashSize // IPv4 + port + node_id = 54
)

// Bootstrap seed nodes.
var SeedNodes = []string{
	"lbrynet3.lbry.com:4444",
	"51.83.238.186:4444",
	"lbrynet1.lbry.com:4444",
	"lbrynet2.lbry.com:4444",
	"lbrynet4.lbry.com:4444",
	"dht.lbry.grin.io:4444",
	"dht.lizard.technology:4444",
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
	selfID  [HashSize]byte
	buckets [HashSize * 8]KBucket // 384 buckets
	mu      sync.RWMutex
}

func NewRoutingTable(selfID [HashSize]byte) *RoutingTable {
	return &RoutingTable{selfID: selfID}
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

func (rt *RoutingTable) AddPeer(p Peer) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	idx := rt.bucketIndex(p.ID)
	bucket := &rt.buckets[idx]

	// Check if already in bucket
	for i, existing := range bucket.peers {
		if existing.ID == p.ID {
			bucket.peers[i].LastSeen = time.Now()
			bucket.peers[i].IP = p.IP
			bucket.peers[i].UDPPort = p.UDPPort
			if p.TCPPort > 0 {
				bucket.peers[i].TCPPort = p.TCPPort
			}
			return
		}
	}

	if len(bucket.peers) < K {
		p.LastSeen = time.Now()
		bucket.peers = append(bucket.peers, p)
	}
	// If full, drop (simplified — full Kademlia pings least-recent)
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
	ID          [HashSize]byte
	UDPPort     int
	TCPPort     int // blob exchange port
	conn        *net.UDPConn
	routing     *RoutingTable
	pending     map[string]chan map[string]any // rpcID -> response channel
	mu          sync.RWMutex
	running     bool
	tokenSecret [HashSize]byte // random secret for token generation
}

func NewNode(udpPort int) (*Node, error) {
	var id [HashSize]byte
	rand.Read(id[:])

	var secret [HashSize]byte
	rand.Read(secret[:])

	node := &Node{
		ID:          id,
		UDPPort:     udpPort,
		routing:     NewRoutingTable(id),
		pending:     make(map[string]chan map[string]any),
		tokenSecret: secret,
	}
	return node, nil
}

func (n *Node) Start() error {
	addr := &net.UDPAddr{Port: n.UDPPort}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("dht: listen UDP: %w", err)
	}
	n.conn = conn
	n.running = true
	n.UDPPort = conn.LocalAddr().(*net.UDPAddr).Port

	go n.readLoop()
	go n.refreshLoop()

	log.Printf("DHT node started on UDP port %d (ID: %s...)", n.UDPPort, hex.EncodeToString(n.ID[:8]))
	return nil
}

// refreshLoop periodically refreshes the routing table.
func (n *Node) refreshLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for n.running {
		<-ticker.C
		log.Println("DHT: refreshing routing table...")
		// Lookup a random ID to discover new peers
		var randomID [HashSize]byte
		rand.Read(randomID[:])
		n.FindNode(randomID)
		// Also re-lookup own ID
		n.FindNode(n.ID)
	}
}

func (n *Node) Stop() {
	n.running = false
	if n.conn != nil {
		n.conn.Close()
	}
}

// readLoop handles incoming UDP messages.
func (n *Node) readLoop() {
	buf := make([]byte, MsgSizeLimit+100)
	for n.running {
		n.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		nBytes, remoteAddr, err := n.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		go n.handleMessage(buf[:nBytes], remoteAddr)
	}
}

func (n *Node) handleMessage(data []byte, from *net.UDPAddr) {
	decoded, _, err := BencodeDecode(data)
	if err != nil {
		return
	}
	msg, ok := decoded.(map[string]any)
	if !ok {
		return
	}

	packetType := getBencInt(msg, "0")
	rpcID := getBencBytes(msg, "1")
	nodeID := getBencBytes(msg, "2")

	if len(nodeID) == HashSize {
		var id [HashSize]byte
		copy(id[:], nodeID)
		n.routing.AddPeer(Peer{ID: id, IP: from.IP, UDPPort: from.Port})
	}

	switch packetType {
	case 0: // REQUEST
		n.handleRequest(msg, from)
	case 1: // RESPONSE
		key := string(rpcID)
		n.mu.RLock()
		ch, ok := n.pending[key]
		n.mu.RUnlock()
		if ok {
			ch <- msg
		}
	}
}

func (n *Node) handleRequest(msg map[string]any, from *net.UDPAddr) {
	method := string(getBencBytes(msg, "3"))
	rpcID := getBencBytes(msg, "1")

	switch method {
	case "ping":
		n.sendResponse(from, rpcID, []byte("pong"))
	case "findNode":
		args := getBencList(msg, "4")
		if len(args) == 0 {
			return
		}
		key := toBytes(args[0])
		if len(key) != HashSize {
			return
		}
		var keyArr [HashSize]byte
		copy(keyArr[:], key)
		peers := n.routing.ClosestPeers(keyArr, K)
		contacts := peersToContactList(peers)
		n.sendResponse(from, rpcID, contacts)
	case "findValue":
		// We don't store values, just return closest nodes
		args := getBencList(msg, "4")
		if len(args) == 0 {
			return
		}
		key := toBytes(args[0])
		if len(key) != HashSize {
			return
		}
		var keyArr [HashSize]byte
		copy(keyArr[:], key)
		peers := n.routing.ClosestPeers(keyArr, K)
		contacts := peersToContactList(peers)

		// Generate token
		token := n.makeToken(from.IP)
		resp := map[string]any{
			"token":    string(token),
			"contacts": contacts,
		}
		n.sendResponse(from, rpcID, resp)
	}
}

func (n *Node) makeToken(ip net.IP) []byte {
	h := sha512.New384()
	h.Write(n.tokenSecret[:])
	h.Write(ip.To4())
	return h.Sum(nil)
}

func (n *Node) sendResponse(to *net.UDPAddr, rpcID []byte, value any) {
	msg := map[int]any{
		0: 1, // RESPONSE
		1: rpcID,
		2: n.ID[:],
		3: value,
	}
	data, err := BencodeEncode(msg)
	if err != nil {
		return
	}
	n.conn.WriteToUDP(data, to)
}

// sendRPC sends a request and waits for response.
func (n *Node) sendRPC(to *net.UDPAddr, method string, args []any) (map[string]any, error) {
	rpcID := make([]byte, RPCIDSize)
	rand.Read(rpcID)

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

	msg := map[int]any{
		0: 0, // REQUEST
		1: rpcID,
		2: n.ID[:],
		3: []byte(method),
		4: args,
	}

	data, err := BencodeEncode(msg)
	if err != nil {
		return nil, err
	}

	ch := make(chan map[string]any, 1)
	key := string(rpcID)
	n.mu.Lock()
	n.pending[key] = ch
	n.mu.Unlock()

	defer func() {
		n.mu.Lock()
		delete(n.pending, key)
		n.mu.Unlock()
	}()

	n.conn.WriteToUDP(data, to)

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(RPCTimeout):
		return nil, fmt.Errorf("rpc timeout")
	}
}

// Ping sends a ping to a peer.
func (n *Node) Ping(addr *net.UDPAddr) error {
	_, err := n.sendRPC(addr, "ping", nil)
	return err
}

// FindNode performs an iterative find_node lookup.
func (n *Node) FindNode(key [HashSize]byte) []Peer {
	return n.iterativeLookup(key, false)
}

// FindValue performs an iterative find_value lookup, returning blob peers.
// Returns (blob_peers, closest_nodes).
func (n *Node) FindValue(key [HashSize]byte) ([]Peer, []Peer) {
	var blobPeers []Peer
	var mu sync.Mutex

	closest := n.iterativeLookupWithCallback(key, true, func(resp map[string]any) {
		// Check if response contains blob peers
		respVal := getBencAny(resp, "3")
		respDict, ok := respVal.(map[string]any)
		if !ok {
			return
		}

		// The blob peers are stored under a key that matches the blob hash.
		// The key format varies: could be raw bytes as string, hex string, or anything.
		// Check all dict keys that aren't known protocol fields.
		knownKeys := map[string]bool{"token": true, "contacts": true, "p": true, "protocolVersion": true}
		var peersData any
		for k, v := range respDict {
			if knownKeys[k] {
				continue
			}
			// Any unknown key with a list value is likely the blob peers
			if _, isList := v.([]any); isList {
				peersData = v
				break
			}
		}

		if peerList, ok := peersData.([]any); ok {
			mu.Lock()
			for _, p := range peerList {
				peerBytes := toBytes(p)
				if len(peerBytes) >= 6 { // at least IP + port
					ip := net.IPv4(peerBytes[0], peerBytes[1], peerBytes[2], peerBytes[3])
					tcpPort := int(binary.BigEndian.Uint16(peerBytes[4:6]))
					var nodeID [HashSize]byte
					if len(peerBytes) >= CompactAddrSize {
						copy(nodeID[:], peerBytes[6:])
					}
					blobPeers = append(blobPeers, Peer{
						ID: nodeID, IP: ip, TCPPort: tcpPort,
					})
				}
			}
			mu.Unlock()
		}
	})

	return blobPeers, closest
}

// iterativeLookup performs the Kademlia iterative lookup.
func (n *Node) iterativeLookup(key [HashSize]byte, findValue bool) []Peer {
	return n.iterativeLookupWithCallback(key, findValue, nil)
}

func (n *Node) iterativeLookupWithCallback(key [HashSize]byte, findValue bool, onResponse func(map[string]any)) []Peer {
	shortlist := n.routing.ClosestPeers(key, K)
	if len(shortlist) == 0 {
		return nil
	}

	contacted := make(map[[HashSize]byte]bool)
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
				method := "findNode"
				args := []any{key[:]}
				if findValue {
					method = "findValue"
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
				continue
			}
			if onResponse != nil {
				onResponse(r.resp)
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

	if len(shortlist) > K {
		shortlist = shortlist[:K]
	}
	return shortlist
}

// Bootstrap connects to seed nodes and populates the routing table.
func (n *Node) Bootstrap() error {
	log.Println("DHT: bootstrapping...")
	pinged := 0
	for _, seed := range SeedNodes {
		addr, err := net.ResolveUDPAddr("udp4", seed)
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

// --- Helpers ---

func peersToContactList(peers []Peer) []any {
	var list []any
	for _, p := range peers {
		ip4 := p.IP.To4()
		if ip4 == nil {
			continue
		}
		contact := []any{p.ID[:], ip4, p.UDPPort}
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

		if len(nodeIDBytes) != HashSize || port <= 0 {
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
