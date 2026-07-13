package config

import "encoding/json"

// Kind identifies the Python SDK setting descriptor whose behavior a setting
// must preserve.
type Kind uint8

const (
	KindString Kind = iota
	KindPath
	KindInteger
	KindFloat
	KindToggle
	KindMaxKeyFee
	KindStringChoice
	KindServers
	KindStrings
)

type Server struct {
	Host string
	Port any
}

func (server Server) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]any{server.Host, server.Port})
}

type Paths struct {
	Config      string
	DataDir     string
	DownloadDir string
	WalletDir   string
}

// BigInteger carries Python integer values that do not fit in a native Go int.
// Its JSON and YAML encoders emit a numeric scalar rather than a quoted string.
type BigInteger string

type Spec struct {
	Name          string
	Kind          Kind
	Default       any
	PreviousNames []string
	Choices       []string
}

func server(host string, port int) Server {
	return Server{Host: host, Port: port}
}

func defaultSpecs(paths Paths) []Spec {
	return []Spec{
		{Name: "allowed_origin", Kind: KindString, Default: ""},
		{
			Name:          "announce_head_and_sd_only",
			Kind:          KindToggle,
			Default:       true,
			PreviousNames: []string{"announce_head_blobs_only"},
		},
		{Name: "api", Kind: KindString, Default: "localhost:5279"},
		{Name: "audio_encoder", Kind: KindString, Default: "aac -b:a 160k"},
		{Name: "blob_download_timeout", Kind: KindFloat, Default: 30.0},
		{Name: "blob_lru_cache_size", Kind: KindInteger, Default: 32},
		{Name: "blob_storage_limit", Kind: KindInteger, Default: 0},
		{Name: "blockchain_name", Kind: KindString, Default: "lbrycrd_main"},
		{
			Name:    "coin_selection_strategy",
			Kind:    KindStringChoice,
			Default: "prefer_confirmed",
			Choices: []string{
				"sqlite",
				"prefer_confirmed",
				"only_confirmed",
				"standard",
				"branch_and_bound",
				"closest_match",
				"random_draw",
			},
		},
		{Name: "components_to_skip", Kind: KindStrings, Default: []string{}},
		{
			Name:          "concurrent_blob_announcers",
			Kind:          KindInteger,
			Default:       10,
			PreviousNames: []string{"concurrent_announcers"},
		},
		{Name: "concurrent_hub_requests", Kind: KindInteger, Default: 32},
		{Name: "concurrent_reflector_uploads", Kind: KindInteger, Default: 10},
		{Name: "config", Kind: KindPath, Default: paths.Config},
		{Name: "data_dir", Kind: KindPath, Default: paths.DataDir},
		{
			Name:          "download_dir",
			Kind:          KindPath,
			Default:       paths.DownloadDir,
			PreviousNames: []string{"download_directory"},
		},
		{Name: "download_timeout", Kind: KindFloat, Default: 30.0},
		{
			Name:          "ffmpeg_path",
			Kind:          KindString,
			Default:       "",
			PreviousNames: []string{"ffmpeg_folder"},
		},
		{Name: "fixed_peer_delay", Kind: KindFloat, Default: 2.0},
		{
			Name:    "fixed_peers",
			Kind:    KindServers,
			Default: []Server{server("cdn.reflector.lbry.com", 5567)},
		},
		{Name: "hub_timeout", Kind: KindFloat, Default: 30.0},
		{Name: "is_bootstrap_node", Kind: KindToggle, Default: false},
		{Name: "jurisdiction", Kind: KindString, Default: nil},
		{
			Name: "known_dht_nodes",
			Kind: KindServers,
			Default: []Server{
				server("dht.lbry.grin.io", 4444),
				server("dht.lbry.madiator.com", 4444),
				server("dht.lbry.pigg.es", 4444),
				server("lbrynet1.lbry.com", 4444),
				server("lbrynet2.lbry.com", 4444),
				server("lbrynet3.lbry.com", 4444),
				server("lbrynet4.lbry.com", 4444),
				server("dht.lizard.technology", 4444),
				server("s2.lbry.network", 4444),
			},
		},
		{
			Name: "lbryum_servers",
			Kind: KindServers,
			Default: []Server{
				server("spv11.lbry.com", 50001),
				server("spv12.lbry.com", 50001),
				server("spv13.lbry.com", 50001),
				server("spv14.lbry.com", 50001),
				server("spv15.lbry.com", 50001),
				server("spv16.lbry.com", 50001),
				server("spv17.lbry.com", 50001),
				server("spv18.lbry.com", 50001),
				server("spv19.lbry.com", 50001),
				server("hub.lbry.grin.io", 50001),
				server("hub.lizard.technology", 50001),
				server("s1.lbry.network", 50001),
			},
		},
		{
			Name:          "max_connections_per_download",
			Kind:          KindInteger,
			Default:       4,
			PreviousNames: []string{"max_connections_per_stream"},
		},
		{
			Name:    "max_key_fee",
			Kind:    KindMaxKeyFee,
			Default: map[string]any{"currency": "USD", "amount": 50.0},
		},
		{Name: "max_wallet_server_fee", Kind: KindString, Default: "0.0"},
		{Name: "network_interface", Kind: KindString, Default: "0.0.0.0"},
		{Name: "network_storage_limit", Kind: KindInteger, Default: 0},
		{Name: "node_rpc_timeout", Kind: KindFloat, Default: 5.0},
		{Name: "peer_connect_timeout", Kind: KindFloat, Default: 3.0},
		{Name: "prometheus_port", Kind: KindInteger, Default: 0},
		{
			Name:          "reflect_streams",
			Kind:          KindToggle,
			Default:       true,
			PreviousNames: []string{"reflect_uploads"},
		},
		{
			Name:    "reflector_servers",
			Kind:    KindServers,
			Default: []Server{server("reflector.lbry.com", 5566)},
		},
		{Name: "save_blobs", Kind: KindToggle, Default: true},
		{Name: "save_files", Kind: KindToggle, Default: false},
		{Name: "save_resolved_claims", Kind: KindToggle, Default: true},
		{
			Name:          "share_usage_data",
			Kind:          KindToggle,
			Default:       false,
			PreviousNames: []string{"upload_log", "upload_log", "share_debug_info"},
		},
		{Name: "split_buckets_under_index", Kind: KindInteger, Default: 2},
		{Name: "streaming_get", Kind: KindToggle, Default: true},
		{Name: "streaming_server", Kind: KindString, Default: "localhost:5280"},
		{
			Name:          "tcp_port",
			Kind:          KindInteger,
			Default:       4444,
			PreviousNames: []string{"peer_port"},
		},
		{Name: "track_bandwidth", Kind: KindToggle, Default: true},
		{
			Name: "tracker_servers",
			Kind: KindServers,
			Default: []Server{
				server("tracker.lbry.com", 9252),
				server("tracker.lbry.grin.io", 9252),
				server("tracker.lbry.pigg.es", 9252),
				server("tracker.lizard.technology", 9252),
				server("s1.lbry.network", 9252),
			},
		},
		{Name: "transaction_cache_size", Kind: KindInteger, Default: 131072},
		{
			Name:          "udp_port",
			Kind:          KindInteger,
			Default:       4444,
			PreviousNames: []string{"dht_node_port"},
		},
		{Name: "use_upnp", Kind: KindToggle, Default: true},
		{Name: "video_bitrate_maximum", Kind: KindInteger, Default: 5000000},
		{
			Name:    "video_encoder",
			Kind:    KindString,
			Default: "libx264 -crf 24 -preset faster -pix_fmt yuv420p",
		},
		{
			Name:    "video_scaler",
			Kind:    KindString,
			Default: `-vf "scale=if(gte(iw\,ih)\,min(1920\,iw)\,-2):if(lt(iw\,ih)\,min(1920\,ih)\,-2)" -maxrate 5500K -bufsize 5000K`,
		},
		{Name: "volume_analysis_time", Kind: KindInteger, Default: 240},
		{Name: "volume_filter", Kind: KindString, Default: ""},
		{
			Name:          "wallet_dir",
			Kind:          KindPath,
			Default:       paths.WalletDir,
			PreviousNames: []string{"lbryum_wallet_dir"},
		},
		{Name: "wallets", Kind: KindStrings, Default: []string{"default_wallet"}},
	}
}
