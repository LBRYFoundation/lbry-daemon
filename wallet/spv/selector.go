package spv

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	SPVStatusMagic           = uint32(1446058291)
	SPVStatusProtocolVersion = byte(1)
	SPVPingSize              = 69
	SPVPongSize              = 44
	DefaultProbeTimeout      = 3 * time.Second
	DefaultDNSCacheTTL       = 300 * time.Second
)

var (
	ErrInvalidSPVPing    = errors.New("invalid SPV status ping")
	ErrInvalidSPVPong    = errors.New("invalid SPV status pong")
	ErrUnknownCountry    = errors.New("unknown SPV country code")
	ErrNoResolvedServers = errors.New("no SPV servers resolved")
)

type Ping struct {
	Version byte
	Padding [64]byte
}

func MakePing() []byte {
	packet := make([]byte, SPVPingSize)
	binary.BigEndian.PutUint32(packet[:4], SPVStatusMagic)
	packet[4] = SPVStatusProtocolVersion
	return packet
}

func DecodePing(packet []byte) (Ping, error) {
	if len(packet) < SPVPingSize {
		return Ping{}, fmt.Errorf("%w: got %d bytes, need %d", ErrInvalidSPVPing, len(packet), SPVPingSize)
	}
	if binary.BigEndian.Uint32(packet[:4]) != SPVStatusMagic {
		return Ping{}, fmt.Errorf("%w: invalid magic bytes", ErrInvalidSPVPing)
	}
	var ping Ping
	ping.Version = packet[4]
	copy(ping.Padding[:], packet[5:69])
	return ping, nil
}

type Pong struct {
	ProtocolVersion byte
	Flags           byte
	Height          uint32
	Tip             [32]byte
	SourceAddress   [4]byte
	Country         uint16
}

func DecodePong(packet []byte) (Pong, error) {
	if len(packet) < SPVPongSize {
		return Pong{}, fmt.Errorf("%w: got %d bytes, need %d", ErrInvalidSPVPong, len(packet), SPVPongSize)
	}
	var pong Pong
	pong.ProtocolVersion = packet[0]
	pong.Flags = packet[1]
	pong.Height = binary.BigEndian.Uint32(packet[2:6])
	copy(pong.Tip[:], packet[6:38])
	copy(pong.SourceAddress[:], packet[38:42])
	pong.Country = binary.BigEndian.Uint16(packet[42:44])
	return pong, nil
}

func (pong Pong) Encode() []byte {
	packet := make([]byte, SPVPongSize)
	packet[0] = pong.ProtocolVersion
	packet[1] = pong.Flags
	binary.BigEndian.PutUint32(packet[2:6], pong.Height)
	copy(packet[6:38], pong.Tip[:])
	copy(packet[38:42], pong.SourceAddress[:])
	binary.BigEndian.PutUint16(packet[42:44], pong.Country)
	return packet
}

func (pong Pong) Available() bool { return pong.Flags&1 != 0 }

func (pong Pong) SourceIP() string {
	return net.IPv4(
		pong.SourceAddress[0], pong.SourceAddress[1], pong.SourceAddress[2], pong.SourceAddress[3],
	).String()
}

func (pong Pong) CountryName() (string, error) { return CountryName(pong.Country) }

type Candidate struct {
	Server Server
	Pong   *Pong
}

type SelectionRequest struct {
	Servers      []Server
	KnownHubs    *KnownHubs
	Jurisdiction *string
}

type CandidateSelector interface {
	Select(context.Context, SelectionRequest) ([]Candidate, error)
}

type SequentialSelector struct{}

func (SequentialSelector) Select(ctx context.Context, request SelectionRequest) ([]Candidate, error) {
	if ctx == nil {
		return nil, errors.New("SPV selection context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidates := make([]Candidate, len(request.Servers))
	for index, server := range request.Servers {
		candidates[index] = Candidate{Server: server}
	}
	return candidates, nil
}

type IPv4Resolver interface {
	ResolveIPv4(context.Context, Server) (net.IP, error)
}

type cachedDNSResult struct {
	ip      net.IP
	expires time.Time
}

type CachedIPv4Resolver struct {
	mu       sync.Mutex
	resolver *net.Resolver
	ttl      time.Duration
	cache    map[Server]cachedDNSResult
}

func NewCachedIPv4Resolver(resolver *net.Resolver, ttl time.Duration) *CachedIPv4Resolver {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if ttl == 0 {
		ttl = DefaultDNSCacheTTL
	}
	return &CachedIPv4Resolver{
		resolver: resolver,
		ttl:      ttl,
		cache:    make(map[Server]cachedDNSResult),
	}
}

func (resolver *CachedIPv4Resolver) ResolveIPv4(ctx context.Context, server Server) (net.IP, error) {
	if ctx == nil {
		return nil, errors.New("SPV DNS context is nil")
	}
	if strings.EqualFold(server.Host, "localhost") {
		return net.IPv4(127, 0, 0, 1), nil
	}
	if parsed := net.ParseIP(server.Host); parsed != nil {
		if ipv4 := parsed.To4(); ipv4 != nil {
			return append(net.IP(nil), ipv4...), nil
		}
		return nil, fmt.Errorf("SPV UDP requires IPv4, got %q", server.Host)
	}
	now := time.Now()
	resolver.mu.Lock()
	if cached, exists := resolver.cache[server]; exists && now.Before(cached.expires) {
		ip := append(net.IP(nil), cached.ip...)
		resolver.mu.Unlock()
		return ip, nil
	}
	resolver.mu.Unlock()
	addresses, err := resolver.resolver.LookupIP(ctx, "ip4", server.Host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 || addresses[0].To4() == nil {
		return nil, fmt.Errorf("no IPv4 address for %q", server.Host)
	}
	ip := append(net.IP(nil), addresses[0].To4()...)
	resolver.mu.Lock()
	resolver.cache[server] = cachedDNSResult{ip: append(net.IP(nil), ip...), expires: now.Add(resolver.ttl)}
	resolver.mu.Unlock()
	return ip, nil
}

type ListenPacketFunc func(context.Context) (net.PacketConn, error)

type UDPSelector struct {
	Timeout     time.Duration
	Resolver    IPv4Resolver
	Listen      ListenPacketFunc
	RandomIndex func(int) int
}

func NewUDPSelector() *UDPSelector {
	return &UDPSelector{Resolver: NewCachedIPv4Resolver(nil, DefaultDNSCacheTTL)}
}

type resolvedEndpoint struct {
	ip      net.IP
	port    int
	aliases []string
}

type endpointKey struct {
	ip   string
	port int
}

func (selector *UDPSelector) Select(ctx context.Context, request SelectionRequest) ([]Candidate, error) {
	if ctx == nil {
		return nil, errors.New("SPV selection context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := selector.Timeout
	if timeout == 0 {
		timeout = DefaultProbeTimeout
	}
	resolver := selector.Resolver
	if resolver == nil {
		resolver = NewCachedIPv4Resolver(nil, DefaultDNSCacheTTL)
	}
	endpoints := resolveProbeEndpoints(ctx, resolver, request.Servers)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listen := selector.Listen
	if listen == nil {
		listen = listenUDP4
	}
	connection, err := listen(ctx)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	stopCancellation := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-stopCancellation:
		}
	}()
	defer func() {
		close(stopCancellation)
		_ = connection.Close()
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(endpoints) == 0 {
		return []Candidate{}, nil
	}

	byRemote := make(map[endpointKey]resolvedEndpoint, len(endpoints))
	ping := MakePing()
	start := time.Now()
	for _, endpoint := range endpoints {
		key := endpointKey{ip: endpoint.ip.String(), port: endpoint.port}
		byRemote[key] = endpoint
		if _, err := connection.WriteTo(ping, &net.UDPAddr{IP: endpoint.ip, Port: endpoint.port}); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, err
		}
	}
	deadline := start.Add(timeout)
	if contextDeadline, exists := ctx.Deadline(); exists && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	available := make([]Candidate, 0, len(endpoints))
	seenAvailable := make(map[endpointKey]struct{}, len(endpoints))
	buffer := make([]byte, 64*1024)
	for len(seenAvailable) < len(endpoints) {
		length, remote, err := connection.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var networkErr net.Error
			if errors.As(err, &networkErr) && networkErr.Timeout() {
				break
			}
			return nil, err
		}
		udpRemote, ok := remote.(*net.UDPAddr)
		if !ok || udpRemote.IP.To4() == nil {
			continue
		}
		key := endpointKey{ip: udpRemote.IP.To4().String(), port: udpRemote.Port}
		endpoint, expected := byRemote[key]
		if !expected || len(endpoint.aliases) == 0 {
			continue
		}
		pong, err := DecodePong(buffer[:length])
		if err != nil {
			continue
		}
		country, err := pong.CountryName()
		if err != nil {
			continue
		}
		canonical := Server{Host: endpoint.aliases[0], Port: endpoint.port}
		if request.KnownHubs != nil {
			request.KnownHubs.UpdateCountry(canonical, country)
		}
		if !pong.Available() {
			continue
		}
		if _, duplicate := seenAvailable[key]; duplicate {
			continue
		}
		seenAvailable[key] = struct{}{}
		copyPong := pong
		available = append(available, Candidate{Server: canonical, Pong: &copyPong})
	}
	if len(available) == 0 {
		index := 0
		if selector.RandomIndex != nil {
			index = selector.RandomIndex(len(endpoints))
		} else {
			index = rand.Intn(len(endpoints))
		}
		if index < 0 || index >= len(endpoints) {
			return nil, fmt.Errorf("SPV fallback index %d is out of range", index)
		}
		endpoint := endpoints[index]
		fallback := Server{Host: endpoint.ip.String(), Port: endpoint.port}
		if request.KnownHubs != nil {
			request.KnownHubs.Set(fallback, HubDetails{})
		}
		return []Candidate{{Server: fallback}}, nil
	}
	if request.Jurisdiction == nil {
		return available, nil
	}
	filtered := make([]Candidate, 0, len(available))
	for _, candidate := range available {
		country, err := candidate.Pong.CountryName()
		if err == nil && country == *request.Jurisdiction {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}

func listenUDP4(context.Context) (net.PacketConn, error) {
	return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
}

type resolutionResult struct {
	server Server
	ip     net.IP
	err    error
}

func resolveProbeEndpoints(ctx context.Context, resolver IPv4Resolver, servers []Server) []resolvedEndpoint {
	results := make(chan resolutionResult, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			ip, err := resolver.ResolveIPv4(ctx, server)
			results <- resolutionResult{server: server, ip: ip, err: err}
		}()
	}
	endpoints := make([]resolvedEndpoint, 0, len(servers))
	indexes := make(map[endpointKey]int, len(servers))
	for range servers {
		result := <-results
		if result.err != nil || result.ip.To4() == nil {
			continue
		}
		ip := append(net.IP(nil), result.ip.To4()...)
		key := endpointKey{ip: ip.String(), port: result.server.Port}
		if index, exists := indexes[key]; exists {
			endpoints[index].aliases = append(endpoints[index].aliases, result.server.Host)
			continue
		}
		indexes[key] = len(endpoints)
		endpoints = append(endpoints, resolvedEndpoint{
			ip: ip, port: result.server.Port, aliases: []string{result.server.Host},
		})
	}
	return endpoints
}

func CountryName(code uint16) (string, error) {
	names := countryNames()
	if int(code) >= len(names) {
		return "", fmt.Errorf("%w: %d", ErrUnknownCountry, code)
	}
	name := names[code]
	if strings.HasPrefix(name, "R") {
		return name[1:], nil
	}
	return name, nil
}

func CountryCode(name string) (uint16, error) {
	lookup := name
	if len(lookup) == 3 {
		lookup = "R" + lookup
	}
	for index, candidate := range countryNames() {
		if candidate == lookup {
			return uint16(index), nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownCountry, name)
}

var (
	countryNamesOnce sync.Once
	countryNamesList []string
)

func countryNames() []string {
	countryNamesOnce.Do(func() {
		countryNamesList = strings.FieldsFunc(countryNamesText, func(character rune) bool {
			return character == ',' || character == '\n' || character == '\r' || character == ' ' || character == '\t'
		})
	})
	return countryNamesList
}

const countryNamesText = `
UNKNOWN_COUNTRY,AF,AX,AL,DZ,AS,AD,AO,AI,AQ,AG,AR,AM,AW,AU,AT,AZ,BS,BH,BD,BB,BY,BE,BZ,BJ,BM,BT,BO,BQ,BA,BW,BV,BR,IO,BN,BG,BF,BI,KH,CM,CA,CV,KY,CF,TD,CL,CN,CX,CC,CO,KM,CG,CD,CK,CR,CI,HR,CU,CW,CY,CZ,DK,DJ,DM,DO,EC,EG,SV,GQ,ER,EE,ET,FK,FO,FJ,FI,FR,GF,PF,TF,GA,GM,GE,DE,GH,GI,GR,GL,GD,GP,GU,GT,GG,GN,GW,GY,HT,HM,VA,HN,HK,HU,IS,IN,ID,IR,IQ,IE,IM,IL,IT,JM,JP,JE,JO,KZ,KE,KI,KP,KR,KW,KG,LA,LV,LB,LS,LR,LY,LI,LT,LU,MO,MK,MG,MW,MY,MV,ML,MT,MH,MQ,MR,MU,YT,MX,FM,MD,MC,MN,ME,MS,MA,MZ,MM,NA,NR,NP,NL,NC,NZ,NI,NE,NG,NU,NF,MP,NO,OM,PK,PW,PS,PA,PG,PY,PE,PH,PN,PL,PT,PR,QA,RE,RO,RU,RW,BL,SH,KN,LC,MF,PM,VC,WS,SM,ST,SA,SN,RS,SC,SL,SG,SX,SK,SI,SB,SO,ZA,GS,SS,ES,LK,SD,SR,SJ,SZ,SE,CH,SY,TW,TJ,TZ,TH,TL,TG,TK,TO,TT,TN,TR,TM,TC,TV,UG,UA,AE,GB,US,UM,UY,UZ,VU,VE,VN,VG,VI,WF,EH,YE,ZM,ZW,
R001,R002,R015,R012,R818,R434,R504,R729,R788,R732,R202,R014,R086,R108,R174,R262,R232,R231,R260,R404,R450,R454,R480,R175,R508,R638,R646,R690,R706,R728,R800,R834,R894,R716,R017,R024,R120,R140,R148,R178,R180,R226,R266,R678,R018,R072,R748,R426,R516,R710,R011,R204,R854,R132,R384,R270,R288,R324,R624,R430,R466,R478,R562,R566,R654,R686,R694,R768,R019,R419,R029,R660,R028,R533,R044,R052,R535,R092,R136,R192,R531,R212,R214,R308,R312,R332,R388,R474,R500,R630,R652,R659,R662,R663,R670,R534,R780,R796,R850,R013,R084,R188,R222,R320,R340,R484,R558,R591,R005,R032,R068,R074,R076,R152,R170,R218,R238,R254,R328,R600,R604,R239,R740,R858,R862,R021,R060,R124,R304,R666,R840,R010,R142,R143,R398,R417,R762,R795,R860,R030,R156,R344,R446,R408,R392,R496,R410,R035,R096,R116,R360,R418,R458,R104,R608,R702,R764,R626,R704,R034,R004,R050,R064,R356,R364,R462,R524,R586,R144,R145,R051,R031,R048,R196,R268,R368,R376,R400,R414,R422,R512,R634,R682,R275,R760,R792,R784,R887,R150,R151,R112,R100,R203,R348,R616,R498,R642,R643,R703,R804,R154,R248,R830,R831,R832,R680,R208,R233,R234,R246,R352,R372,R833,R428,R440,R578,R744,R752,R826,R039,R008,R020,R070,R191,R292,R300,R336,R380,R470,R499,R807,R620,R674,R688,R705,R724,R155,R040,R056,R250,R276,R438,R442,R492,R528,R756,R009,R053,R036,R162,R166,R334,R554,R574,R054,R242,R540,R598,R090,R548,R057,R316,R296,R584,R583,R520,R580,R585,R581,R061,R016,R184,R258,R570,R612,R882,R772,R776,R798,R876
`

var _ CandidateSelector = SequentialSelector{}
var _ CandidateSelector = (*UDPSelector)(nil)
var _ IPv4Resolver = (*CachedIPv4Resolver)(nil)
