package rpc

import "bufio"
import "bytes"
import "context"
import cryptorand "crypto/rand"
import "encoding/base64"
import "encoding/hex"
import "encoding/json"
import "errors"
import "fmt"
import "io"
import daemonconfig "lbry/daemon/config"
import "lbry/daemon/wallet/ledgerdb"
import "log"
import "math"
import "math/big"
import "math/rand"
import "mime"
import "net"
import "net/http"
import "runtime/debug"
import "slices"
import "strconv"
import "strings"
import "sync"
import "unicode/utf16"
import "unicode/utf8"

import walletpkg "lbry/daemon/wallet"
import spvpkg "lbry/daemon/wallet/spv"
import databasepkg "lbry/daemon/database"
import blobpkg "lbry/daemon/blob"
import dhtpkg "lbry/daemon/dht"

import "google.golang.org/protobuf/encoding/protowire"
import "golang.org/x/text/encoding/ianaindex"

type RPCServer struct {
	httpServer            *http.Server
	handlers              map[string]func(http.ResponseWriter, any)
	allowedOriginOverride *string
	settings              SettingsStore
	status                StatusProvider
	shutdown              func()
	walletManagerProvider func() *walletpkg.WalletManager
	resolvedClaimSaver    ResolvedClaimSaver
	managedFileLister     ManagedFileLister
	managedBlobCleaner    ManagedBlobCleaner
	managedFileController ManagedFileController
	blobManager           *blobpkg.BlobManager
	exchangeRates         ExchangeRateConverter
	dhtNodeProvider       func() *dhtpkg.Node
	fileAnalyzer          FileAnalyzer
	stopOnce              sync.Once
	platformInfo          map[string]any
	traceMu               sync.Mutex
	traceEnabled          bool
	getMu                 sync.Mutex
	getFlights            map[string]*managedGetFlight
	walletMutationMu      sync.Mutex
}

type managedGetFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	result  map[string]any
	err     error
}

const legacyRPCMaxRequestBodySize int64 = 1024 * 1024

type SettingsStore interface {
	Snapshot() map[string]any
	Get(string) (any, bool)
	Set(string, any) (any, error)
	Clear(string) (any, error)
}

type StatusProvider interface {
	Status() map[string]any
}

type ExchangeRateConverter interface {
	ToDewies(currency, amount string) (uint64, error)
}

type FileAnalyzer interface {
	Status(context.Context, bool, bool) map[string]any
	VerifyOrRepair(context.Context, bool, bool, string, bool) (string, map[string]any, error)
}

// ResolvedClaimSaver is the legacy lbrynet.sqlite persistence boundary used
// after resolve succeeds and before its outputs are encoded.
type ResolvedClaimSaver interface {
	SaveResolvedClaims(context.Context, *walletpkg.Ledger, []*walletpkg.TransactionOutput) error
}

type SupportSaver interface {
	SaveSupports(context.Context, string, []databasepkg.SupportRow) error
}

type ManagedFileLister interface {
	ListManagedFiles(context.Context) ([]databasepkg.ManagedFileRow, error)
}

type ManagedFileStore interface {
	ManagedFileLister
	ChangeManagedFileStatus(context.Context, string, string) error
	ChangeManagedFilePath(context.Context, string, *string, *string) error
	SetManagedFileSaved(context.Context, string, bool) error
	DeleteManagedStream(context.Context, string) ([]string, error)
}

type ManagedDownloadStore interface {
	ManagedFileStore
	SaveResolvedClaims(context.Context, *walletpkg.Ledger, []*walletpkg.TransactionOutput) error
	SaveStreamDescriptor(context.Context, string, int, *blobpkg.StreamDescriptor, int64, bool) error
	SaveManagedFile(context.Context, string, *string, *string, float64, string, []byte, int64) (int64, error)
	LinkManagedStreamClaim(context.Context, string, string) error
	MarkManagedBlobsFinished(context.Context, []string) error
}

type ManagedReflectorStore interface {
	MarkStreamReflected(context.Context, string, string) error
}

type ManagedBlobCleaner interface {
	CleanManagedBlobs(context.Context, *blobpkg.BlobManager, int64, int64) (int, error)
}

type ManagedFileController interface {
	StartManagedFile(context.Context, databasepkg.ManagedFileRow) error
	StopManagedFile(context.Context, databasepkg.ManagedFileRow) error
	SaveManagedFile(
		context.Context, databasepkg.ManagedFileRow, *string, *string,
	) (databasepkg.ManagedFileRow, error)
}

type ManagedFileRegistrar interface {
	RegisterManagedFile(context.Context, databasepkg.ManagedFileRow) error
}

type ManagedFileDeletionController interface {
	PrepareManagedFileDelete(context.Context, databasepkg.ManagedFileRow) error
	FinishManagedFileDelete(databasepkg.ManagedFileRow, bool)
}

type ManagedAnnouncementStore interface {
	QueueBlobAnnouncements(context.Context, []string, bool) error
}

type ServerOption func(*RPCServer)

func WithAllowedOrigin(origin string) ServerOption {
	return func(server *RPCServer) {
		server.allowedOriginOverride = &origin
	}
}

func WithSettingsStore(settings SettingsStore) ServerOption {
	return func(server *RPCServer) {
		server.settings = settings
	}
}

func WithStatusProvider(status StatusProvider) ServerOption {
	return func(server *RPCServer) {
		server.status = status
	}
}

func WithShutdown(shutdown func()) ServerOption {
	return func(server *RPCServer) {
		server.shutdown = shutdown
	}
}

func WithWalletManagerProvider(provider func() *walletpkg.WalletManager) ServerOption {
	return func(server *RPCServer) {
		server.walletManagerProvider = provider
	}
}

func WithResolvedClaimSaver(saver ResolvedClaimSaver) ServerOption {
	return func(server *RPCServer) {
		server.resolvedClaimSaver = saver
	}
}

func WithManagedFileLister(lister ManagedFileLister) ServerOption {
	return func(server *RPCServer) {
		server.managedFileLister = lister
	}
}

func WithManagedBlobCleaner(cleaner ManagedBlobCleaner) ServerOption {
	return func(server *RPCServer) {
		server.managedBlobCleaner = cleaner
	}
}

func WithManagedFileController(controller ManagedFileController) ServerOption {
	return func(server *RPCServer) {
		server.managedFileController = controller
	}
}

func WithBlobManager(manager *blobpkg.BlobManager) ServerOption {
	return func(server *RPCServer) {
		server.blobManager = manager
	}
}

func WithExchangeRateConverter(converter ExchangeRateConverter) ServerOption {
	return func(server *RPCServer) {
		server.exchangeRates = converter
	}
}

func WithDHTNodeProvider(provider func() *dhtpkg.Node) ServerOption {
	return func(server *RPCServer) { server.dhtNodeProvider = provider }
}

func WithFileAnalyzer(analyzer FileAnalyzer) ServerOption {
	return func(server *RPCServer) { server.fileAnalyzer = analyzer }
}

func CreateServer(options ...ServerOption) *RPCServer {
	server := &RPCServer{
		handlers:     copyHandlers(handlers),
		settings:     daemonconfig.NewMemory(),
		platformInfo: collectPlatformInfo(),
	}
	for _, option := range options {
		option(server)
	}
	server.httpServer = &http.Server{Handler: server}
	server.handlers["version"] = func(w http.ResponseWriter, _ any) {
		sendResultResponse(w, copyMap(server.platformInfo))
	}
	server.handlers["settings_get"] = server.handleSettingsGet
	server.handlers["settings_set"] = server.handleSettingsSet
	server.handlers["settings_clear"] = server.handleSettingsClear
	server.handlers["claim_search"] = server.handleClaimSearch
	server.handlers["resolve"] = server.handleResolve
	server.handlers["ffmpeg_find"] = server.handleFFmpegFind
	server.handlers["support_sum"] = server.handleSupportSum
	server.handlers["tracemalloc_enable"] = server.handleTracemallocEnable
	server.handlers["tracemalloc_disable"] = server.handleTracemallocDisable
	server.handlers["tracemalloc_top"] = server.handleTracemallocTop
	if server.status != nil {
		server.handlers["status"] = server.handleStatus
	}
	if server.walletManagerProvider != nil {
		server.handlers["preference_get"] = server.handlePreferenceGet
		server.handlers["preference_set"] = server.handlePreferenceSet
		server.handlers["account_list"] = server.handleAccountList
		server.handlers["account_balance"] = server.handleAccountBalance
		server.handlers["account_add"] = server.handleAccountAdd
		server.handlers["account_create"] = server.handleAccountCreate
		server.handlers["account_remove"] = server.handleAccountRemove
		server.handlers["account_set"] = server.handleAccountSet
		server.handlers["account_max_address_gap"] = server.handleAccountMaxAddressGap
		server.handlers["account_fund"] = server.handleAccountFund
		server.handlers["account_deposit"] = server.handleAccountDeposit
		server.handlers["account_send"] = server.handleAccountSend
		server.handlers["sync_hash"] = server.handleSyncHash
		server.handlers["sync_apply"] = server.handleSyncApply
		server.handlers["address_is_mine"] = server.handleAddressIsMine
		server.handlers["address_list"] = server.handleAddressList
		server.handlers["address_unused"] = server.handleAddressUnused
		server.handlers["claim_list"] = server.handleClaimList
		server.handlers["channel_list"] = server.handleChannelList
		server.handlers["channel_abandon"] = server.handleChannelAbandon
		server.handlers["channel_create"] = server.handleChannelCreate
		server.handlers["channel_export"] = server.handleChannelExport
		server.handlers["channel_import"] = server.handleChannelImport
		server.handlers["channel_sign"] = server.handleChannelSign
		server.handlers["channel_update"] = server.handleChannelUpdate
		server.handlers["collection_list"] = server.handleCollectionList
		server.handlers["collection_abandon"] = server.handleCollectionAbandon
		server.handlers["collection_create"] = server.handleCollectionCreate
		server.handlers["collection_update"] = server.handleCollectionUpdate
		server.handlers["collection_resolve"] = server.handleCollectionResolve
		server.handlers["purchase_list"] = server.handlePurchaseList
		server.handlers["purchase_create"] = server.handlePurchaseCreate
		server.handlers["publish"] = server.handlePublish
		server.handlers["stream_list"] = server.handleStreamList
		server.handlers["stream_abandon"] = server.handleStreamAbandon
		server.handlers["stream_create"] = server.handleStreamCreate
		server.handlers["stream_repost"] = server.handleStreamRepost
		server.handlers["stream_update"] = server.handleStreamUpdate
		server.handlers["stream_cost_estimate"] = server.handleStreamCostEstimate
		server.handlers["support_list"] = server.handleSupportList
		server.handlers["support_create"] = server.handleSupportCreate
		server.handlers["support_abandon"] = server.handleSupportAbandon
		server.handlers["transaction_list"] = server.handleTransactionList
		server.handlers["transaction_show"] = server.handleTransactionShow
		server.handlers["txo_list"] = server.handleTXOList
		server.handlers["txo_sum"] = server.handleTXOSum
		server.handlers["txo_spend"] = server.handleTXOSpend
		server.handlers["txo_plot"] = server.handleTXOPlot
		server.handlers["wallet_decrypt"] = server.handleWalletDecrypt
		server.handlers["wallet_balance"] = server.handleWalletBalance
		server.handlers["wallet_encrypt"] = server.handleWalletEncrypt
		server.handlers["wallet_lock"] = server.handleWalletLock
		server.handlers["wallet_list"] = server.handleWalletList
		server.handlers["wallet_status"] = server.handleWalletStatus
		server.handlers["wallet_unlock"] = server.handleWalletUnlock
		server.handlers["wallet_reconnect"] = server.handleWalletReconnect
		server.handlers["wallet_export"] = server.handleWalletExport
		server.handlers["wallet_import"] = server.handleWalletImport
		server.handlers["wallet_send"] = server.handleWalletSend
		server.handlers["wallet_create"] = server.handleWalletCreate
		server.handlers["wallet_add"] = server.handleWalletAdd
		server.handlers["wallet_remove"] = server.handleWalletRemove
		server.handlers["utxo_list"] = server.handleUTXOList
		server.handlers["utxo_release"] = server.handleUTXORelease
	}
	if server.walletManagerProvider != nil && server.managedFileLister != nil {
		server.handlers["file_list"] = server.handleFileList
		server.handlers["file_set_status"] = server.handleFileSetStatus
		server.handlers["file_save"] = server.handleFileSave
		server.handlers["file_delete"] = server.handleFileDelete
		if server.blobManager != nil {
			server.handlers["file_reflect"] = server.handleFileReflect
		}
	}
	if server.walletManagerProvider != nil && server.managedFileLister != nil && server.blobManager != nil {
		server.handlers["get"] = server.handleGet
	}
	if server.blobManager != nil {
		server.handlers["blob_get"] = server.handleBlobGet
		server.handlers["blob_delete"] = server.handleBlobDelete
		server.handlers["blob_list"] = server.handleBlobList
		if _, ok := server.managedFileLister.(ManagedBlobCleaner); ok {
			server.handlers["blob_clean"] = server.handleBlobClean
		}
	}
	if server.dhtNodeProvider != nil {
		server.handlers["peer_ping"] = server.handlePeerPing
		server.handlers["peer_list"] = server.handlePeerList
		server.handlers["routing_table_get"] = server.handleRoutingTableGet
		if server.blobManager != nil {
			server.handlers["blob_announce"] = server.handleBlobAnnounce
		}
	}
	for _, name := range []string{
		"preference_set", "account_add", "account_create", "account_remove", "account_set",
		"account_max_address_gap", "account_fund", "account_deposit", "account_send",
		"sync_apply", "channel_abandon", "channel_create", "channel_import", "channel_update",
		"collection_abandon", "collection_create", "collection_update", "purchase_create",
		"publish", "stream_abandon", "stream_create", "stream_repost", "stream_update",
		"support_abandon", "support_create", "txo_spend", "utxo_release",
		"wallet_add", "wallet_create", "wallet_decrypt", "wallet_encrypt", "wallet_import",
		"wallet_lock", "wallet_reconnect", "wallet_remove", "wallet_send", "wallet_unlock",
	} {
		handler, ok := server.handlers[name]
		if !ok {
			continue
		}
		server.handlers[name] = func(response http.ResponseWriter, params any) {
			server.walletMutationMu.Lock()
			defer server.walletMutationMu.Unlock()
			handler(response, params)
		}
	}

	return server
}

func (rpcServer *RPCServer) handleStatus(w http.ResponseWriter, _ any) {
	sendResultResponse(w, rpcServer.status.Status())
}

func (rpcServer *RPCServer) Handler() http.Handler {
	return rpcServer
}

func (rpcServer *RPCServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/":
		switch req.Method {
		case http.MethodPost:
			rpcServer.handleJSONRPC(w, req)
		case http.MethodOptions:
			rpcServer.handleCORS(w, req)
		default:
			w.Header().Set("Allow", "OPTIONS,POST")
			writePlainHTTPError(w, http.StatusMethodNotAllowed)
		}
	case "/lbryapi":
		switch req.Method {
		case http.MethodGet, http.MethodPost:
			rpcServer.handleJSONRPC(w, req)
		case http.MethodHead:
			rpcServer.handleJSONRPC(headResponseWriter{ResponseWriter: w}, req)
		default:
			w.Header().Set("Allow", "GET,HEAD,POST")
			writePlainHTTPError(w, http.StatusMethodNotAllowed)
		}
	default:
		writePlainHTTPError(w, http.StatusNotFound)
	}
}

type headResponseWriter struct {
	http.ResponseWriter
}

func (writer headResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

type bufferedResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (writer *bufferedResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *bufferedResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}

func (writer *bufferedResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.body.Write(data)
}

func (writer *bufferedResponseWriter) flushTo(destination http.ResponseWriter) {
	for name, values := range writer.header {
		destination.Header()[name] = append([]string(nil), values...)
	}
	status := writer.status
	if status == 0 {
		status = http.StatusOK
	}
	destination.WriteHeader(status)
	_, _ = destination.Write(writer.body.Bytes())
}

func writePlainHTTPError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "%d: %s", status, http.StatusText(status))
}

func copyHandlers(source map[string]func(http.ResponseWriter, any)) map[string]func(http.ResponseWriter, any) {
	cloned := make(map[string]func(http.ResponseWriter, any), len(source))
	for name, handler := range source {
		cloned[name] = handler
	}
	return cloned
}

func copyMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}

func (rpcServer *RPCServer) StartServer(listener net.Listener) {
	err := rpcServer.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		log.Printf("error starting RPC server: %v", err)
	}
}

func (rpcServer *RPCServer) Serve(listener net.Listener) error {
	return rpcServer.httpServer.Serve(listener)
}

func (rpcServer *RPCServer) Shutdown(ctx context.Context) error {
	err := rpcServer.httpServer.Shutdown(ctx)
	if err != nil {
		_ = rpcServer.httpServer.Close()
	}
	return err
}

func sendResultResponse(w http.ResponseWriter, result any) {
	writeJSON(w, map[string]any{
		"jsonrpc": "2.0",
		"result":  result,
	})
}

func sendErrorResponse(w http.ResponseWriter, code int, message string) {
	sendErrorResponseWithData(w, code, message, map[string]any{})
}

func sendErrorResponseWithData(w http.ResponseWriter, code int, message string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	writeJSON(w, map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    code,
			"data":    data,
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoded, err := encodeJSON(payload)
	if err != nil {
		encoded, _ = encodeJSON(map[string]any{
			"jsonrpc": "2.0",
			"error": map[string]any{
				"code":    -32500,
				"data":    map[string]any{"traceback": err.Error()},
				"message": "After successfully executing the command, failed to encode result for JSON RPC response.",
			},
		})
	}
	_, _ = w.Write(encoded)
}

func encodeJSON(payload any) ([]byte, error) {
	tokens, err := newSpecialFloatTokens()
	if err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(allowSpecialJSONFloats(payload, tokens)); err != nil {
		return nil, err
	}
	data := encoded.Bytes()
	for placeholder, literal := range map[string]string{
		tokens.nan:         "NaN",
		tokens.infinity:    "Infinity",
		tokens.negInfinity: "-Infinity",
	} {
		encodedPlaceholder, _ := json.Marshal(placeholder)
		data = bytes.ReplaceAll(data, encodedPlaceholder, []byte(literal))
	}
	return escapeNonASCII(data), nil
}

type specialFloatTokens struct {
	nan         string
	infinity    string
	negInfinity string
}

func newSpecialFloatTokens() (specialFloatTokens, error) {
	var nonce [16]byte
	if _, err := cryptorand.Read(nonce[:]); err != nil {
		return specialFloatTokens{}, err
	}
	prefix := "\x00LBRY_JSON_" + hex.EncodeToString(nonce[:]) + "_"
	return specialFloatTokens{
		nan:         prefix + "NAN\x00",
		infinity:    prefix + "INFINITY\x00",
		negInfinity: prefix + "NEG_INFINITY\x00",
	}, nil
}

func quoteLegacySpecialJSONFloats(body []byte) ([]byte, specialFloatTokens, error) {
	tokens, err := newSpecialFloatTokens()
	if err != nil {
		return nil, specialFloatTokens{}, err
	}
	replacements := []struct {
		literal string
		quoted  []byte
	}{
		{literal: "-Infinity", quoted: mustMarshalJSONString(tokens.negInfinity)},
		{literal: "Infinity", quoted: mustMarshalJSONString(tokens.infinity)},
		{literal: "NaN", quoted: mustMarshalJSONString(tokens.nan)},
	}

	var converted bytes.Buffer
	inString := false
	containers := make([]legacyJSONContainer, 0)
	for index := 0; index < len(body); {
		if inString {
			converted.WriteByte(body[index])
			if body[index] == '\\' && index+1 < len(body) {
				index++
				converted.WriteByte(body[index])
			} else if body[index] == '"' {
				inString = false
			}
			index++
			continue
		}
		if body[index] == '"' {
			inString = true
			converted.WriteByte(body[index])
			index++
			continue
		}
		switch body[index] {
		case '{':
			containers = append(containers, legacyJSONContainer{kind: '{', expectsKey: true})
		case '[':
			containers = append(containers, legacyJSONContainer{kind: '['})
		case ':':
			if len(containers) > 0 && containers[len(containers)-1].kind == '{' {
				containers[len(containers)-1].expectsKey = false
			}
		case ',':
			if len(containers) > 0 && containers[len(containers)-1].kind == '{' {
				containers[len(containers)-1].expectsKey = true
			}
		case '}', ']':
			if len(containers) > 0 {
				containers = containers[:len(containers)-1]
			}
		}

		replaced := false
		for _, replacement := range replacements {
			end := index + len(replacement.literal)
			if end <= len(body) && string(body[index:end]) == replacement.literal &&
				legacyJSONExpectsValue(containers) && legacyJSONValueBoundary(body, index, end) {
				converted.Write(replacement.quoted)
				index = end
				replaced = true
				break
			}
		}
		if !replaced {
			converted.WriteByte(body[index])
			index++
		}
	}
	return converted.Bytes(), tokens, nil
}

func mustMarshalJSONString(value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

type legacyJSONContainer struct {
	kind       byte
	expectsKey bool
}

func legacyJSONExpectsValue(containers []legacyJSONContainer) bool {
	if len(containers) == 0 {
		return true
	}
	container := containers[len(containers)-1]
	return container.kind == '[' || !container.expectsKey
}

func legacyJSONValueBoundary(body []byte, start, end int) bool {
	before := start == 0 || strings.ContainsRune("[{,: \t\r\n", rune(body[start-1]))
	after := end == len(body) || strings.ContainsRune("]}, \t\r\n", rune(body[end]))
	return before && after
}

func restoreLegacySpecialJSONFloats(value any, tokens specialFloatTokens) any {
	switch typed := value.(type) {
	case string:
		switch typed {
		case tokens.nan:
			return math.NaN()
		case tokens.infinity:
			return math.Inf(1)
		case tokens.negInfinity:
			return math.Inf(-1)
		default:
			return typed
		}
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			floating, parseErr := strconv.ParseFloat(typed.String(), 64)
			if parseErr != nil && math.IsInf(floating, 0) {
				return floating
			}
			return json.Number(pythonFloatJSON(floating))
		}
		return typed
	case map[string]any:
		for name, item := range typed {
			typed[name] = restoreLegacySpecialJSONFloats(item, tokens)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = restoreLegacySpecialJSONFloats(item, tokens)
		}
		return typed
	default:
		return value
	}
}

func allowSpecialJSONFloats(value any, tokens specialFloatTokens) any {
	switch typed := value.(type) {
	case float64:
		switch {
		case math.IsNaN(typed):
			return tokens.nan
		case math.IsInf(typed, 1):
			return tokens.infinity
		case math.IsInf(typed, -1):
			return tokens.negInfinity
		default:
			return typed
		}
	case float32:
		return allowSpecialJSONFloats(float64(typed), tokens)
	case map[string]any:
		converted := make(map[string]any, len(typed))
		for name, item := range typed {
			converted[name] = allowSpecialJSONFloats(item, tokens)
		}
		return converted
	case []any:
		converted := make([]any, len(typed))
		for index, item := range typed {
			converted[index] = allowSpecialJSONFloats(item, tokens)
		}
		return converted
	default:
		return value
	}
}

func escapeNonASCII(data []byte) []byte {
	var escaped strings.Builder
	for len(data) > 0 {
		runeValue, size := utf8.DecodeRune(data)
		data = data[size:]
		if runeValue <= 0x7f {
			escaped.WriteRune(runeValue)
			continue
		}
		if runeValue <= 0xffff {
			_, _ = fmt.Fprintf(&escaped, "\\u%04x", runeValue)
			continue
		}
		high, low := utf16.EncodeRune(runeValue)
		_, _ = fmt.Fprintf(&escaped, "\\u%04x\\u%04x", high, low)
	}
	return []byte(escaped.String())
}

func (rpcServer *RPCServer) setCORSHeaders(w http.ResponseWriter) {
	allowedOrigin := rpcServer.allowedOrigin()
	if allowedOrigin == "" {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
	w.Header().Set("Access-Control-Allow-Methods", allowedOrigin)
	w.Header().Set("Access-Control-Allow-Headers", allowedOrigin)
}

func (rpcServer *RPCServer) handleCORS(w http.ResponseWriter, _ *http.Request) {
	if rpcServer.allowedOrigin() == "" {
		panic(http.ErrAbortHandler)
	}
	rpcServer.setCORSHeaders(w)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
}

func (rpcServer *RPCServer) requestAllowed(req *http.Request) bool {
	origin := req.Header.Get("Origin")
	allowedOrigin := rpcServer.allowedOrigin()
	return origin == "" || origin == allowedOrigin || allowedOrigin == "*"
}

func (rpcServer *RPCServer) allowedOrigin() string {
	if rpcServer.allowedOriginOverride != nil {
		return *rpcServer.allowedOriginOverride
	}
	if rpcServer.settings != nil {
		if value, exists := rpcServer.settings.Get("allowed_origin"); exists {
			if origin, ok := value.(string); ok {
				return origin
			}
		}
	}
	return ""
}

func (rpcServer *RPCServer) handleJSONRPC(w http.ResponseWriter, req *http.Request) {
	if !rpcServer.requestAllowed(req) {
		writePlainHTTPError(w, http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, legacyRPCMaxRequestBodySize))
	if err != nil {
		writeLegacyInternalServerError(w)
		return
	}
	if int64(len(body)) >= legacyRPCMaxRequestBodySize {
		actualSize := int64(len(body))
		if req.ContentLength >= legacyRPCMaxRequestBodySize {
			actualSize = req.ContentLength
		}
		writeRequestEntityTooLarge(w, actualSize)
		return
	}
	body, err = decodeLegacyRequestBody(req, body)
	if err != nil {
		writeLegacyInternalServerError(w)
		return
	}
	decodedBody, inputFloatTokens, err := quoteLegacySpecialJSONFloats(body)
	if err != nil {
		writeLegacyInternalServerError(w)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(decodedBody))
	decoder.UseNumber()
	var message map[string]any
	if err := decoder.Decode(&message); err != nil {
		writeLegacyInternalServerError(w)
		return
	}
	if message == nil {
		writeLegacyInternalServerError(w)
		return
	}
	message, _ = restoreLegacySpecialJSONFloats(message, inputFloatTokens).(map[string]any)
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeLegacyInternalServerError(w)
		return
	}
	if paramsOrder := extractParamsOrder(decodedBody); len(paramsOrder) > 0 {
		message[paramsOrderKey] = paramsOrder
	}
	if method, exists := message["method"]; exists {
		switch method.(type) {
		case []any, map[string]any:
			writeLegacyInternalServerError(w)
			return
		}
	}

	response := newBufferedResponseWriter()
	requestStop := rpcServer.handleJSONRPCMessage(response, message, req.Context())
	rpcServer.setCORSHeaders(response)
	response.flushTo(w)
	if requestStop && rpcServer.shutdown != nil {
		go rpcServer.stopOnce.Do(rpcServer.shutdown)
	}
}

func writeRequestEntityTooLarge(w http.ResponseWriter, actualSize int64) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	_, _ = fmt.Fprintf(
		w, "Maximum request body size %d exceeded, actual body size %d",
		legacyRPCMaxRequestBodySize, actualSize,
	)
}

func decodeLegacyRequestBody(req *http.Request, body []byte) ([]byte, error) {
	charset := "utf-8"
	if contentType := req.Header.Get("Content-Type"); contentType != "" {
		_, parameters, err := mime.ParseMediaType(contentType)
		if err == nil && parameters["charset"] != "" {
			charset = parameters["charset"]
		}
	}
	if strings.EqualFold(charset, "utf-8") || strings.EqualFold(charset, "utf8") {
		if !utf8.Valid(body) {
			return nil, errors.New("invalid UTF-8 request body")
		}
		return body, nil
	}
	if strings.EqualFold(charset, "ascii") || strings.EqualFold(charset, "us-ascii") {
		for _, value := range body {
			if value > 0x7f {
				return nil, errors.New("invalid ASCII request body")
			}
		}
		return body, nil
	}
	encoding, err := ianaindex.MIME.Encoding(charset)
	if err != nil {
		return nil, err
	}
	if encoding == nil {
		return nil, fmt.Errorf("unknown request charset %q", charset)
	}
	decoded, err := encoding.NewDecoder().Bytes(body)
	if err != nil || !utf8.Valid(decoded) {
		return nil, errors.New("invalid encoded request body")
	}
	return decoded, nil
}

func extractParamsOrder(body []byte) []string {
	var rawMessage map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMessage); err != nil {
		return nil
	}
	rawParams, exists := rawMessage["params"]
	if !exists {
		return nil
	}
	trimmed := bytes.TrimSpace(rawParams)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '{' {
		return orderedObjectKeys(trimmed)
	}
	if trimmed[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil
	}
	if len(items) == 1 && len(bytes.TrimSpace(items[0])) > 0 && bytes.TrimSpace(items[0])[0] == '{' {
		return orderedObjectKeys(items[0])
	}
	if len(items) == 2 && len(bytes.TrimSpace(items[1])) > 0 && bytes.TrimSpace(items[1])[0] == '{' {
		return orderedObjectKeys(items[1])
	}
	return nil
}

func orderedObjectKeys(data []byte) []string {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	keys := make([]string, 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil
		}
		key, ok := token.(string)
		if !ok {
			return nil
		}
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil
		}
	}
	return keys
}

func writeLegacyInternalServerError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, "500 Internal Server Error\n\nServer got itself in trouble")
}

var handlers = map[string]func(http.ResponseWriter, any){
	"account_add":             handleJSONRPCMessageAccountAdd,
	"account_balance":         handleJSONRPCMessageAccountBalance,
	"account_create":          handleJSONRPCMessageAccountCreate,
	"account_deposit":         handleJSONRPCMessageAccountDeposit,
	"account_fund":            handleJSONRPCMessageAccountFund,
	"account_list":            handleJSONRPCMessageAccountList,
	"account_max_address_gap": handleJSONRPCMessageAccountMaxAddressGap,
	"account_remove":          handleJSONRPCMessageAccountRemove,
	"account_send":            handleJSONRPCMessageAccountSend,
	"account_set":             handleJSONRPCMessageAccountSet,
	"address_is_mine":         handleJSONRPCMessageAddressIsMine,
	"address_list":            handleJSONRPCMessageAddressList,
	"address_unused":          handleJSONRPCMessageAddressUnused,
	"blob_announce":           handleJSONRPCMessageBlobAnnounce,
	"blob_clean":              handleJSONRPCMessageBlobClean,
	"blob_delete":             handleJSONRPCMessageBlobDelete,
	"blob_get":                handleJSONRPCMessageBlobGet,
	"blob_list":               handleJSONRPCMessageBlobList,
	"blob_reflect":            handleJSONRPCMessageBlobReflect,
	"blob_reflect_all":        handleJSONRPCMessageBlobReflectAll,
	"channel_abandon":         handleJSONRPCMessageChannelAbandon,
	"channel_create":          handleJSONRPCMessageChannelCreate,
	"channel_export":          handleJSONRPCMessageChannelExport,
	"channel_import":          handleJSONRPCMessageChannelImport,
	"channel_list":            handleJSONRPCMessageChannelList,
	"channel_sign":            handleJSONRPCMessageChannelSign,
	"channel_update":          handleJSONRPCMessageChannelUpdate,
	"claim_list":              handleJSONRPCMessageClaimList,
	"claim_search":            handleJSONRPCMessageClaimSearch,
	"collection_abandon":      handleJSONRPCMessageCollectionAbandon,
	"collection_create":       handleJSONRPCMessageCollectionCreate,
	"collection_list":         handleJSONRPCMessageCollectionList,
	"collection_resolve":      handleJSONRPCMessageCollectionResolve,
	"collection_update":       handleJSONRPCMessageCollectionUpdate,
	"ffmpeg_find":             handleJSONRPCMessageFfmpegFind,
	"file_delete":             handleJSONRPCMessageFileDelete,
	"file_list":               handleJSONRPCMessageFileList,
	"file_reflect":            handleJSONRPCMessageFileReflect,
	"file_save":               handleJSONRPCMessageFileSave,
	"file_set_status":         handleJSONRPCMessageFileSetStatus,
	"get":                     handleJSONRPCMessageGet,
	"peer_list":               handleJSONRPCMessagePeerList,
	"peer_ping":               handleJSONRPCMessagePeerPing,
	"preference_get":          handleJSONRPCMessagePreferenceGet,
	"preference_set":          handleJSONRPCMessagePreferenceSet,
	"publish":                 handleJSONRPCMessagePublish,
	"purchase_create":         handleJSONRPCMessagePurchaseCreate,
	"purchase_list":           handleJSONRPCMessagePurchaseList,
	"resolve":                 handleJSONRPCMessageResolve,
	"routing_table_get":       handleJSONRPCMessageRoutingTableGet,
	"settings_clear":          handleJSONRPCMessageSettingsClear,
	"settings_get":            handleJSONRPCMessageSettingsGet,
	"settings_set":            handleJSONRPCMessageSettingsSet,
	"status":                  handleJSONRPCMessageStatus,
	"stop":                    handleJSONRPCMessageStop,
	"stream_abandon":          handleJSONRPCMessageStreamAbandon,
	"stream_cost_estimate":    handleJSONRPCMessageStreamCostEstimate,
	"stream_create":           handleJSONRPCMessageStreamCreate,
	"stream_list":             handleJSONRPCMessageStreamList,
	"stream_repost":           handleJSONRPCMessageStreamRepost,
	"stream_update":           handleJSONRPCMessageStreamUpdate,
	"support_abandon":         handleJSONRPCMessageSupportAbandon,
	"support_create":          handleJSONRPCMessageSupportCreate,
	"support_list":            handleJSONRPCMessageSupportList,
	"support_sum":             handleJSONRPCMessageSupportSum,
	"sync_apply":              handleJSONRPCMessageSyncApply,
	"sync_hash":               handleJSONRPCMessageSyncHash,
	"tracemalloc_disable":     handleJSONRPCMessageTracemallocDisable,
	"tracemalloc_enable":      handleJSONRPCMessageTracemallocEnable,
	"tracemalloc_top":         handleJSONRPCMessageTracemallocTop,
	"transaction_list":        handleJSONRPCMessageTransactionList,
	"transaction_show":        handleJSONRPCMessageTransactionShow,
	"txo_list":                handleJSONRPCMessageTxoList,
	"txo_plot":                handleJSONRPCMessageTxoPlot,
	"txo_spend":               handleJSONRPCMessageTxoSpend,
	"txo_sum":                 handleJSONRPCMessageTxoSum,
	"utxo_list":               handleJSONRPCMessageUtxoList,
	"utxo_release":            handleJSONRPCMessageUtxoRelease,
	"version":                 handleJSONRPCMessageVersion,
	"wallet_add":              handleJSONRPCMessageWalletAdd,
	"wallet_balance":          handleJSONRPCMessageWalletBalance,
	"wallet_create":           handleJSONRPCMessageWalletCreate,
	"wallet_decrypt":          handleJSONRPCMessageWalletDecrypt,
	"wallet_encrypt":          handleJSONRPCMessageWalletEncrypt,
	"wallet_export":           handleJSONRPCMessageWalletExport,
	"wallet_import":           handleJSONRPCMessageWalletImport,
	"wallet_list":             handleJSONRPCMessageWalletList,
	"wallet_lock":             handleJSONRPCMessageWalletLock,
	"wallet_reconnect":        handleJSONRPCMessageWalletReconnect,
	"wallet_remove":           handleJSONRPCMessageWalletRemove,
	"wallet_send":             handleJSONRPCMessageWalletSend,
	"wallet_status":           handleJSONRPCMessageWalletStatus,
	"wallet_unlock":           handleJSONRPCMessageWalletUnlock,
}

func (rpcServer *RPCServer) handleJSONRPCMessage(
	w http.ResponseWriter, message map[string]any, ctx context.Context,
) (requestStop bool) {
	methodValue, existsMethod := message["method"]
	if !existsMethod {
		sendErrorResponse(w, -32601, "Missing 'method' value in request.")
		return false
	}

	method, methodIsString := methodValue.(string)
	if !methodIsString {
		switch methodValue.(type) {
		case []any, map[string]any:
			writeLegacyInternalServerError(w)
		default:
			sendErrorResponse(w, -32601, fmt.Sprintf("Command '%s' does not exist.", pythonStr(methodValue)))
		}
		return false
	}

	resolvedMethod := method
	if replacement, deprecated := deprecatedMethods[method]; deprecated {
		resolvedMethod = replacement
	}
	spec, exists := methodSpecs[resolvedMethod]
	if !exists {
		sendErrorResponse(w, -32601, fmt.Sprintf("Command '%s' does not exist.", method))
		return false
	}

	params, paramsError := normalizeRPCParams(message, spec, method, resolvedMethod)
	if paramsError != nil {
		if paramsError.data != nil {
			sendErrorResponseWithData(w, paramsError.code, paramsError.message, paramsError.data)
		} else {
			sendErrorResponse(w, paramsError.code, paramsError.message)
		}
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	params.ctx = ctx

	handler, exists := rpcServer.handlers[resolvedMethod]
	if !exists {
		sendErrorResponse(w, -32601, fmt.Sprintf("Command '%s' does not exist.", method))
		return false
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			if isRPCRequestCancellation(recovered) {
				panic(http.ErrAbortHandler)
			}
			sendErrorResponseWithData(w, -32500, fmt.Sprint(recovered), map[string]any{
				"args":      params.args,
				"command":   method,
				"kwargs":    redactPassword(params.kwargs),
				"name":      recoveredErrorName(recovered),
				"traceback": currentTraceback(),
			})
		}
	}()

	if resolvedMethod == "stop" {
		sendResultResponse(w, "Shutting down")
		return true
	}
	bufferedResponse := newBufferedResponseWriter()
	handler(bufferedResponse, params)
	bufferedResponse.flushTo(w)
	return false
}

type pythonErrorNamer interface {
	PythonErrorName() string
}

func isRPCRequestCancellation(recovered any) bool {
	err, ok := recovered.(error)
	if !ok {
		return false
	}
	var cancellation *rpcRequestCancellation
	return errors.As(err, &cancellation)
}

func recoveredErrorName(recovered any) string {
	if named, ok := recovered.(pythonErrorNamer); ok {
		return named.PythonErrorName()
	}
	return fmt.Sprintf("%T", recovered)
}

func currentTraceback() []string {
	return strings.Split(strings.TrimSpace(string(debug.Stack())), "\n")
}

func redactPassword(kwargs map[string]any) map[string]any {
	redacted := make(map[string]any, len(kwargs))
	for name, value := range kwargs {
		if name == "password" {
			if password, ok := value.(string); ok {
				value = strings.Repeat("*", utf8.RuneCountInString(password))
			}
		}
		redacted[name] = value
	}
	return redacted
}

func handleJSONRPCMessageAccountAdd(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountBalance(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountDeposit(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountFund(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountMaxAddressGap(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountRemove(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountSend(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountSet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressIsMine(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressUnused(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageBlobAnnounce(w http.ResponseWriter, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageBlobClean(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobDelete(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobGet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobReflect(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobReflectAll(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageChannelAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelExport(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelImport(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelSign(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelUpdate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageClaimList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func SendJSON(host string, port int, req any) (map[string]any, error) {
	conn, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	data, _ := json.Marshal(req)

	conn.Write(append(data, '\n'))

	line, _ := bufio.NewReader(conn).ReadString('\n')
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}

	var resp map[string]any
	json.Unmarshal([]byte(line), &resp)
	return resp, nil
}

func DecodeRawProto(b []byte) (map[int]any, error) {
	m := make(map[int]any)

	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b) // num is protowire.Number
		if n < 0 {
			return nil, fmt.Errorf("invalid tag")
		}
		b = b[n:]

		var val any
		var consumed int

		switch typ {
		case protowire.VarintType:
			val, consumed = protowire.ConsumeVarint(b)
		case protowire.Fixed32Type:
			val, consumed = protowire.ConsumeFixed32(b)
		case protowire.Fixed64Type:
			val, consumed = protowire.ConsumeFixed64(b)
		case protowire.BytesType:
			val, consumed = protowire.ConsumeBytes(b)
			// Recursive decode for nested messages
			if sub, err := DecodeRawProto(val.([]byte)); err == nil && len(sub) > 0 {
				val = sub
			}
		default:
			return nil, fmt.Errorf("unsupported wire type %d for field %d", typ, num)
		}
		if consumed < 0 {
			return nil, fmt.Errorf("consume failed for field %d", num)
		}
		b = b[consumed:]

		// Handle repeating fields (multiple same field number)
		key := int(num) // <-- this fixes the compile error
		if existing, ok := m[key]; ok {
			if slice, ok := existing.([]any); ok {
				m[key] = append(slice, val)
			} else {
				m[key] = []any{existing, val}
			}
		} else {
			m[key] = val
		}
	}
	return m, nil
}

func handleJSONRPCMessageClaimSearch(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageCollectionAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionResolve(w http.ResponseWriter, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageCollectionUpdate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageFfmpegFind(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileDelete(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileReflect(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileSave(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileSetStatus(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageGet(w http.ResponseWriter, params any) {
	// Relaxed
	paramsMap := params.(normalizedRPCParams).named

	uri, _ := paramsMap["uri"].(string)

	resolveResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      rand.Int() + 1,
		"method":  "blockchain.claimtrie.resolve",
		"params":  []string{uri},
	})

	var resolutions map[string]any = map[string]any{}

	_, resultIsString := resolveResp["result"].(string)
	if resultIsString {
		decodedBase64, _ := base64.StdEncoding.DecodeString(resolveResp["result"].(string))

		decodedProtobuf, _ := DecodeRawProto(decodedBase64)

		var resolutionData []any

		_, okResolution := decodedProtobuf[1].([]any)
		if okResolution {
			resolutionData = decodedProtobuf[1].([]any)
		} else {
			resolutionData = []any{decodedProtobuf[1]}
		}

		txIDs := []string{}

		for _, claim := range resolutionData {
			claimMap := claim.(map[int]any)
			txidValue, txidOk := claimMap[1]
			if txidOk {
				txidBytes := txidValue.([]byte)
				slices.Reverse(txidBytes)
				txID := hex.EncodeToString(txidBytes)
				txIDs = append(txIDs, txID)
			}
		}

		transactionResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.transaction.get_batch",
			"params":  txIDs,
		})

		transactionData := transactionResp["result"].(map[string]any)

		for _, claim := range resolutionData {
			claimMap, ok := claim.(map[int]any)
			if ok {
				item := convertProtobufToClaim(claimMap, transactionData)

				resolutionKey := uri
				resolutions[resolutionKey] = item
			}

		}
	}

	sdHash := resolutions[uri].(map[string]any)["value"].(map[string]any)["source"].(map[string]any)["sd_hash"].(string)

	streamingURL := "http://localhost:5280/stream/" + sdHash

	sendResultResponse(w, map[string]any{
		"streaming_url": streamingURL,
	})
}

func handleJSONRPCMessagePeerList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessagePeerPing(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessagePreferenceGet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePreferenceSet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePublish(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePurchaseCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePurchaseList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageResolve(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageRoutingTableGet(w http.ResponseWriter, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageSettingsClear(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageSettingsGet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageSettingsSet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageStatus(w http.ResponseWriter, params any) {
	// Relaxed
	sendResultResponse(w, map[string]any{
		"is_running": true,
	})
}

func handleJSONRPCMessageStop(w http.ResponseWriter, params any) {
	sendResultResponse(w, "Shutting down")
}

func handleJSONRPCMessageStreamAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamCostEstimate(w http.ResponseWriter, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageStreamCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamRepost(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamUpdate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportSum(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSyncApply(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSyncHash(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTracemallocDisable(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageTracemallocEnable(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageTracemallocTop(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageTransactionList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

type transactionListApplicationError struct {
	name    string
	message string
}

func (err transactionListApplicationError) Error() string           { return err.message }
func (err transactionListApplicationError) PythonErrorName() string { return err.name }

func (rpcServer *RPCServer) handleTransactionList(w http.ResponseWriter, params any) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		panic(transactionListApplicationError{
			name:    "ComponentsNotStartedError",
			message: `the following required components have not yet started: ["wallet"]`,
		})
	}
	normalized := params.(normalizedRPCParams)
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		panic(err)
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		panic(transactionListRPCError(err))
	}
	rawAccountID := normalized.named["account_id"]
	if selectedWallet == nil {
		attribute := "get_transaction_history"
		if transactionListTruthy(rawAccountID) {
			attribute = "get_account_or_error"
		}
		panic(transactionListNoneAttributeError(attribute))
	}
	accountID, err := transactionListAccountID(rawAccountID)
	if err != nil {
		panic(err)
	}
	if accountID != nil {
		if _, err := selectedWallet.Account(*accountID); err != nil {
			panic(transactionListRPCError(err))
		}
	}
	if accountID == nil && manager.DefaultLedger() == nil {
		panic(transactionListNoneAttributeError("get_transaction_history"))
	}
	pagination, err := transactionListPaginationParameters(normalized.named)
	if err != nil {
		panic(err)
	}
	result, err := manager.GetTransactionHistoryPage(normalized.ctx, walletpkg.TransactionHistoryPageOptions{
		AccountID: accountID,
		WalletID:  walletID,
		Page:      &pagination.page,
		PageSize:  &pagination.pageSize,
		Offset:    &pagination.offset,
	})
	if err != nil {
		panic(transactionListRPCError(err))
	}
	wireResult := transactionListWireResult(result)
	wireResult["page"] = pagination.wirePage
	wireResult["page_size"] = pagination.wirePageSize
	wireResult["total_pages"] = transactionListPythonTotalPages(
		result.TotalItems, pagination.pageSizeNumber,
	)
	sendResultResponse(w, wireResult)
}

func transactionListWireResult(result walletpkg.TransactionHistoryPage) map[string]any {
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var wireValue map[string]any
	if err := decoder.Decode(&wireValue); err != nil {
		panic(err)
	}
	return wireValue
}

type transactionListPagination struct {
	page           int
	pageSize       int
	offset         int
	wirePage       json.Number
	wirePageSize   json.Number
	pageSizeNumber transactionListPageNumber
}

type transactionListPageNumber struct {
	integer  *big.Int
	floating *float64
	wire     json.Number
}

func transactionListPaginationParameters(values map[string]any) (transactionListPagination, error) {
	page, err := transactionListNormalizedPageNumber(values, "page", 1)
	if err != nil {
		return transactionListPagination{}, err
	}
	pageSize, err := transactionListNormalizedPageNumber(
		values, "page_size", walletpkg.TransactionHistoryDefaultPageSize,
	)
	if err != nil {
		return transactionListPagination{}, err
	}

	offsetNumber, err := transactionListOffsetNumber(page, pageSize)
	if err != nil {
		return transactionListPagination{}, err
	}
	queryPageSize, offset, err := transactionListSQLiteWindow(pageSize, offsetNumber)
	if err != nil {
		return transactionListPagination{}, err
	}
	queryPage, err := transactionListSQLiteInteger(page)
	if err != nil {
		// A fractional page can still produce a valid integral SQLite offset.
		queryPage = 1
	}
	return transactionListPagination{
		page: queryPage, pageSize: queryPageSize, offset: offset,
		wirePage: page.wire, wirePageSize: pageSize.wire, pageSizeNumber: pageSize,
	}, nil
}

func transactionListPythonTotalPages(
	totalItems int64, pageSize transactionListPageNumber,
) json.Number {
	var numerator, denominator float64
	if pageSize.integer != nil {
		numeratorInteger := new(big.Int).Sub(pageSize.integer, big.NewInt(1))
		numeratorInteger.Add(numeratorInteger, big.NewInt(totalItems))
		numerator, _ = new(big.Float).SetInt(numeratorInteger).Float64()
		denominator, _ = new(big.Float).SetInt(pageSize.integer).Float64()
	} else {
		denominator = *pageSize.floating
		numerator = float64(totalItems) + (denominator - 1)
	}
	pages, _ := new(big.Float).SetFloat64(numerator / denominator).Int(nil)
	return json.Number(pages.String())
}

func transactionListNormalizedPageNumber(
	values map[string]any, name string, defaultValue int,
) (transactionListPageNumber, error) {
	value, exists := values[name]
	if !exists || value == nil || !transactionListTruthy(value) {
		return transactionListIntegerPageNumber(int64(defaultValue)), nil
	}
	switch typed := value.(type) {
	case bool:
		return transactionListIntegerPageNumber(1), nil
	case json.Number:
		if !strings.ContainsAny(typed.String(), ".eE") {
			integer, ok := new(big.Int).SetString(typed.String(), 10)
			if ok {
				return transactionListClampIntegerPageNumber(integer, defaultValue), nil
			}
		}
		floating, parseErr := strconv.ParseFloat(typed.String(), 64)
		if parseErr != nil && !math.IsInf(floating, 0) {
			return transactionListPageNumber{}, transactionListPaginationTypeError(value)
		}
		return transactionListClampFloatPageNumber(floating, defaultValue), nil
	case int:
		return transactionListClampIntegerPageNumber(big.NewInt(int64(typed)), defaultValue), nil
	case int64:
		return transactionListClampIntegerPageNumber(big.NewInt(typed), defaultValue), nil
	case float64:
		return transactionListClampFloatPageNumber(typed, defaultValue), nil
	default:
		return transactionListPageNumber{}, transactionListPaginationTypeError(value)
	}
}

func transactionListClampIntegerPageNumber(
	integer *big.Int, defaultValue int,
) transactionListPageNumber {
	if integer.Sign() == 0 {
		return transactionListIntegerPageNumber(int64(defaultValue))
	}
	if integer.Cmp(big.NewInt(1)) <= 0 {
		return transactionListIntegerPageNumber(1)
	}
	return transactionListPageNumber{
		integer: new(big.Int).Set(integer), wire: json.Number(integer.String()),
	}
}

func transactionListClampFloatPageNumber(value float64, defaultValue int) transactionListPageNumber {
	switch {
	case value == 0:
		return transactionListIntegerPageNumber(int64(defaultValue))
	case math.IsNaN(value) || value <= 1:
		return transactionListIntegerPageNumber(1)
	default:
		return transactionListPageNumber{
			floating: &value, wire: json.Number(pythonFloatJSON(value)),
		}
	}
}

func transactionListIntegerPageNumber(value int64) transactionListPageNumber {
	integer := big.NewInt(value)
	return transactionListPageNumber{integer: integer, wire: json.Number(integer.String())}
}

func transactionListOffsetNumber(
	page transactionListPageNumber, pageSize transactionListPageNumber,
) (transactionListPageNumber, error) {
	if page.integer != nil && pageSize.integer != nil {
		pageMinusOne := new(big.Int).Sub(page.integer, big.NewInt(1))
		offset := new(big.Int).Mul(pageSize.integer, pageMinusOne)
		return transactionListPageNumber{integer: offset}, nil
	}
	pageMinusOne, err := transactionListPageMinusOneFloat(page)
	if err != nil {
		return transactionListPageNumber{}, err
	}
	pageSizeValue, err := transactionListPageFloat(pageSize)
	if err != nil {
		return transactionListPageNumber{}, err
	}
	offset := pageSizeValue * pageMinusOne
	return transactionListPageNumber{floating: &offset}, nil
}

func transactionListPageMinusOneFloat(value transactionListPageNumber) (float64, error) {
	if value.floating != nil {
		return *value.floating - 1, nil
	}
	minusOne := new(big.Int).Sub(value.integer, big.NewInt(1))
	floating, _ := new(big.Float).SetInt(minusOne).Float64()
	if math.IsInf(floating, 0) {
		return 0, transactionListApplicationError{
			name: "OverflowError", message: "int too large to convert to float",
		}
	}
	return floating, nil
}

func transactionListSQLiteWindow(
	limit transactionListPageNumber, offset transactionListPageNumber,
) (int, int, error) {
	for _, value := range []transactionListPageNumber{limit, offset} {
		if name, invalid := transactionListSQLiteIdentifier(value); invalid {
			return 0, 0, transactionListApplicationError{
				name: "OperationalError", message: "no such column: " + name,
			}
		}
	}
	queryLimit, err := transactionListSQLiteInteger(limit)
	if err != nil {
		return 0, 0, err
	}
	queryOffset, err := transactionListSQLiteInteger(offset)
	if err != nil {
		return 0, 0, err
	}
	return queryLimit, queryOffset, nil
}

func transactionListSQLiteIdentifier(value transactionListPageNumber) (string, bool) {
	if value.floating == nil {
		return "", false
	}
	switch {
	case math.IsNaN(*value.floating):
		return "nan", true
	case math.IsInf(*value.floating, 0):
		return "inf", true
	default:
		return "", false
	}
}

func transactionListSQLiteInteger(value transactionListPageNumber) (int, error) {
	if value.integer != nil {
		return transactionListBigSQLiteInteger(value.integer)
	}
	return transactionListFloatSQLiteInteger(*value.floating)
}

func transactionListBigSQLiteInteger(value *big.Int) (int, error) {
	if !value.IsInt64() {
		return 0, transactionListSQLiteDatatypeError()
	}
	integer := value.Int64()
	if int64(int(integer)) != integer {
		return 0, transactionListSQLiteDatatypeError()
	}
	return int(integer), nil
}

func transactionListFloatSQLiteInteger(value float64) (int, error) {
	// SQLite LIMIT/OFFSET accepts floats only when they are exact signed-64-bit
	// integers. float64(math.MaxInt64) rounds to 2^63, so use an exclusive bound.
	if math.IsInf(value, 0) {
		return 0, transactionListApplicationError{
			name: "OperationalError", message: "no such column: inf",
		}
	}
	if math.IsNaN(value) || value < 0 ||
		value >= 9223372036854775808 || math.Trunc(value) != value {
		return 0, transactionListSQLiteDatatypeError()
	}
	integer := int64(value)
	if int64(int(integer)) != integer {
		return 0, transactionListSQLiteDatatypeError()
	}
	return int(integer), nil
}

func transactionListPageFloat(value transactionListPageNumber) (float64, error) {
	if value.floating != nil {
		return *value.floating, nil
	}
	floating, _ := new(big.Float).SetInt(value.integer).Float64()
	if math.IsInf(floating, 0) {
		return 0, transactionListApplicationError{
			name: "OverflowError", message: "int too large to convert to float",
		}
	}
	return floating, nil
}

func transactionListSQLiteDatatypeError() error {
	return transactionListApplicationError{name: "IntegrityError", message: "datatype mismatch"}
}

func pythonFloatJSON(value float64) string {
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	if math.IsNaN(value) {
		return "NaN"
	}
	formatted := strconv.FormatFloat(value, 'e', -1, 64)
	exponentIndex := strings.LastIndexByte(formatted, 'e')
	mantissa := formatted[:exponentIndex]
	exponent, _ := strconv.Atoi(formatted[exponentIndex+1:])
	if exponent < -4 || exponent >= 16 {
		return formatted
	}

	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign = "-"
		mantissa = mantissa[1:]
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	decimal := exponent + 1
	switch {
	case decimal <= 0:
		return sign + "0." + strings.Repeat("0", -decimal) + digits
	case decimal >= len(digits):
		return sign + digits + strings.Repeat("0", decimal-len(digits)) + ".0"
	default:
		return sign + digits[:decimal] + "." + digits[decimal:]
	}
}

func transactionListWalletID(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if walletID, ok := value.(string); ok {
		return &walletID, nil
	}
	return nil, transactionListApplicationError{
		name:    "WalletNotLoadedError",
		message: fmt.Sprintf("Wallet %s is not loaded.", pythonStr(value)),
	}
}

func transactionListAccountID(value any) (*string, error) {
	if !transactionListTruthy(value) {
		return nil, nil
	}
	if accountID, ok := value.(string); ok {
		return &accountID, nil
	}
	return nil, transactionListApplicationError{
		name:    "ValueError",
		message: fmt.Sprintf("Couldn't find account: %s.", pythonStr(value)),
	}
}

func transactionListPaginationTypeError(value any) error {
	return transactionListApplicationError{
		name:    "TypeError",
		message: fmt.Sprintf("'>' not supported between instances of '%s' and 'int'", pythonTypeName(value)),
	}
}

func transactionListTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			floating, err := strconv.ParseFloat(typed.String(), 64)
			return err != nil && math.IsInf(floating, 0) || floating != 0
		}
		integer, ok := new(big.Int).SetString(typed.String(), 10)
		return !ok || integer.Sign() != 0
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	case float32:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func transactionListRPCError(err error) error {
	var notLoaded *walletpkg.WalletNotLoadedError
	switch {
	case errors.As(err, &notLoaded):
		return transactionListApplicationError{name: "WalletNotLoadedError", message: err.Error()}
	case strings.HasPrefix(err.Error(), "Couldn't find account:"):
		return transactionListApplicationError{name: "ValueError", message: err.Error()}
	case errors.Is(err, ledgerdb.ErrTransactionAccountsRequired):
		return transactionListApplicationError{name: "AssertionError", message: err.Error()}
	case errors.Is(err, walletpkg.ErrTransactionHistoryPagination):
		return transactionListSQLiteDatatypeError()
	case errors.Is(err, walletpkg.ErrDefaultWalletMissing):
		return transactionListNoneAttributeError("get_transaction_history")
	default:
		return err
	}
}

func transactionListNoneAttributeError(attribute string) error {
	return transactionListApplicationError{
		name:    "AttributeError",
		message: fmt.Sprintf("'NoneType' object has no attribute '%s'", attribute),
	}
}

func handleJSONRPCMessageTransactionShow(w http.ResponseWriter, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func (rpcServer *RPCServer) handleTransactionShow(w http.ResponseWriter, params any) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		panic(transactionListApplicationError{
			name:    "ComponentsNotStartedError",
			message: `the following required components have not yet started: ["wallet"]`,
		})
	}
	normalized := params.(normalizedRPCParams)
	txid, ok := normalized.named["txid"]
	if !ok {
		panic(transactionListApplicationError{
			name:    "TypeError",
			message: "Daemon.jsonrpc_transaction_show() missing 1 required positional argument: 'txid'",
		})
	}
	result, err := manager.GetTransaction(normalized.ctx, txid)
	if err != nil {
		if errors.Is(err, walletpkg.ErrTransactionLookupUnavailable) {
			ledger := manager.DefaultLedger()
			switch {
			case ledger == nil:
				panic(transactionListNoneAttributeError("ledger"))
			case ledger.Database == nil:
				panic(transactionListNoneAttributeError("get_transaction"))
			case ledger.SPVNetwork == nil:
				panic(transactionListNoneAttributeError("get_transaction_and_merkle"))
			}
		}
		if errors.Is(err, spvpkg.ErrConnection) || errors.Is(err, spvpkg.ErrNetworkStopped) {
			panic(transactionListApplicationError{
				name:    "ConnectionError",
				message: "Attempting to send rpc request when connection is not available.",
			})
		}
		panic(err)
	}
	if result.Failure != nil {
		sendResultResponse(w, map[string]any{
			"success": false,
			"code":    result.Failure.Code,
			"message": result.Failure.Message,
		})
		return
	}
	sendResultResponse(w, transactionShowWireValue{
		ledger:          result.Ledger,
		transaction:     result.Transaction,
		includeProtobuf: transactionListTruthy(normalized.includeProtobuf),
	})
}

type transactionShowWireValue struct {
	ledger          *walletpkg.Ledger
	transaction     *walletpkg.Transaction
	includeProtobuf bool
}

func (value transactionShowWireValue) MarshalJSON() ([]byte, error) {
	wire, err := value.ledger.LegacyTransactionJSONWithOptions(
		value.transaction,
		walletpkg.LegacyTransactionJSONOptions{IncludeProtobuf: value.includeProtobuf},
	)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wire)
}

func handleJSONRPCMessageTxoList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoPlot(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoSpend(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoSum(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageUtxoList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageUtxoRelease(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageVersion(w http.ResponseWriter, params any) {
	sendResultResponse(w, collectPlatformInfo())
}

func handleJSONRPCMessageWalletAdd(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletBalance(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletDecrypt(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletEncrypt(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletExport(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletImport(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletLock(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletReconnect(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletRemove(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletSend(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletStatus(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"is_encrypted": nil,
		"is_locked":    nil,
		"is_syncing":   nil,
	})
}

func handleJSONRPCMessageWalletUnlock(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}
