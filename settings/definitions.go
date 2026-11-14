package settings

// The settings the daemon knows about, with the types and defaults of the Python SDK
// (LBRYFoundation/lbry-sdk @ cf4c65b78, lbry/conf.py), so the desktop app, which was written
// against lbrynet, gets the values and shapes it expects.

type kind int

const (
	kindString kind = iota
	kindPath
	kindInt
	kindFloat
	kindBool
	kindStrings
	kindServers
	kindMaxKeyFee
	kindChoice
)

type Definition struct {
	name    string
	kind    kind
	def     any      // default; nil for paths, which depend on the platform (see paths.go)
	choices []string // kindChoice only
	// readOnly settings can be read but not changed through settings_set/settings_clear. They are
	// the ones that decide where the settings file itself lives.
	readOnly bool
}

var currencies = []string{"BTC", "LBC", "USD"}

var coinSelectionStrategies = []string{
	"sqlite", "prefer_confirmed", "only_confirmed", "standard", "branch_and_bound", "closest_match", "random_draw",
}

// server is a host and port, written "host:port" in the settings file and settings_set values.
type Server struct {
	host string
	port int
}

func servers(list ...Server) []Server { return list }

var definitions = []Definition{
	{name: "jurisdiction", kind: kindString, def: ""},

	// directories
	{name: "data_dir", kind: kindPath, readOnly: true},
	{name: "download_dir", kind: kindPath},
	{name: "wallet_dir", kind: kindPath, readOnly: true},
	{name: "wallets", kind: kindStrings, def: []string{"default_wallet"}},

	// network
	{name: "use_upnp", kind: kindBool, def: true},
	{name: "udp_port", kind: kindInt, def: 4444},
	{name: "tcp_port", kind: kindInt, def: 4444},
	{name: "prometheus_port", kind: kindInt, def: 0},
	{name: "network_interface", kind: kindString, def: "0.0.0.0"},

	// routing table
	{name: "split_buckets_under_index", kind: kindInt, def: 2},
	{name: "is_bootstrap_node", kind: kindBool, def: false},

	// protocol timeouts
	{name: "download_timeout", kind: kindFloat, def: 30.0},
	{name: "blob_download_timeout", kind: kindFloat, def: 30.0},
	{name: "hub_timeout", kind: kindFloat, def: 30.0},
	{name: "peer_connect_timeout", kind: kindFloat, def: 3.0},
	{name: "node_rpc_timeout", kind: kindFloat, def: 5.0},

	// blob announcement and download
	{name: "save_blobs", kind: kindBool, def: true},
	{name: "network_storage_limit", kind: kindInt, def: 0},
	{name: "blob_storage_limit", kind: kindInt, def: 0},
	{name: "blob_lru_cache_size", kind: kindInt, def: 32},
	{name: "announce_head_and_sd_only", kind: kindBool, def: true},
	{name: "concurrent_blob_announcers", kind: kindInt, def: 10},
	{name: "max_connections_per_download", kind: kindInt, def: 4},
	{name: "concurrent_hub_requests", kind: kindInt, def: 32},
	{name: "fixed_peer_delay", kind: kindFloat, def: 2.0},
	{name: "max_key_fee", kind: kindMaxKeyFee, def: map[string]any{"currency": "USD", "amount": 50.0}},
	{name: "max_wallet_server_fee", kind: kindString, def: "0.0"},

	// reflector
	{name: "reflect_streams", kind: kindBool, def: true},
	{name: "concurrent_reflector_uploads", kind: kindInt, def: 10},

	// servers
	{name: "reflector_servers", kind: kindServers, def: servers(Server{"reflector.lbry.com", 5566})},
	{name: "fixed_peers", kind: kindServers, def: servers(Server{"cdn.reflector.lbry.com", 5567})},
	{name: "tracker_servers", kind: kindServers, def: servers(
		Server{"tracker.lbry.com", 9252},
		Server{"tracker.lbry.grin.io", 9252},
		Server{"tracker.lbry.pigg.es", 9252},
		Server{"tracker.lizard.technology", 9252},
		Server{"s1.lbry.network", 9252},
	)},
	{name: "lbryum_servers", kind: kindServers, def: servers(
		Server{"spv11.lbry.com", 50001},
		Server{"spv12.lbry.com", 50001},
		Server{"spv13.lbry.com", 50001},
		Server{"spv14.lbry.com", 50001},
		Server{"spv15.lbry.com", 50001},
		Server{"spv16.lbry.com", 50001},
		Server{"spv17.lbry.com", 50001},
		Server{"spv18.lbry.com", 50001},
		Server{"spv19.lbry.com", 50001},
		Server{"hub.lbry.grin.io", 50001},
		Server{"hub.lizard.technology", 50001},
		Server{"s1.lbry.network", 50001},
	)},
	{name: "known_dht_nodes", kind: kindServers, def: servers(
		Server{"dht.lbry.grin.io", 4444},
		Server{"dht.lbry.madiator.com", 4444},
		Server{"dht.lbry.pigg.es", 4444},
		Server{"lbrynet1.lbry.com", 4444},
		Server{"lbrynet2.lbry.com", 4444},
		Server{"lbrynet3.lbry.com", 4444},
		Server{"lbrynet4.lbry.com", 4444},
		Server{"dht.lizard.technology", 4444},
		Server{"s2.lbry.network", 4444},
	)},

	// blockchain
	{name: "blockchain_name", kind: kindString, def: "lbrycrd_main"},

	// daemon
	{name: "save_files", kind: kindBool, def: false},
	{name: "components_to_skip", kind: kindStrings, def: []string{}},
	{name: "share_usage_data", kind: kindBool, def: false},
	{name: "track_bandwidth", kind: kindBool, def: true},
	{name: "allowed_origin", kind: kindString, def: ""},

	// media server
	{name: "streaming_server", kind: kindString, def: "localhost:5280"},
	{name: "streaming_get", kind: kindBool, def: true},
	{name: "coin_selection_strategy", kind: kindChoice, def: "prefer_confirmed", choices: coinSelectionStrategies},
	{name: "transaction_cache_size", kind: kindInt, def: 1 << 17},
	{name: "save_resolved_claims", kind: kindBool, def: true},
}

var definitionsByName = func() map[string]*Definition {
	m := make(map[string]*Definition, len(definitions))
	for i := range definitions {
		m[definitions[i].name] = &definitions[i]
	}
	return m
}()
