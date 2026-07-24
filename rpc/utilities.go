package rpc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleUTXOList(response http.ResponseWriter, params any) {
	normalized := bindTXOWrapperPositional(params.(normalizedRPCParams))
	setTXOWrapperConstraint(&normalized, "type", []any{"other", "purchase"})
	setTXOWrapperConstraint(&normalized, "is_not_spent", true)
	rpcServer.handleTXOList(response, normalized)
}

func (rpcServer *RPCServer) handleUTXORelease(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		panic(err)
	}
	accounts := selectedWallet.Accounts
	if accountID != nil {
		account, err := selectedWallet.Account(*accountID)
		if err != nil {
			panic(err)
		}
		accounts = []*walletpkg.Account{account}
	}
	for _, account := range accounts {
		if err := account.ReleaseAllOutputs(context.WithoutCancel(normalized.ctx)); err != nil {
			panic(err)
		}
	}
	sendResultResponse(response, nil)
}

func (rpcServer *RPCServer) handleFFmpegFind(response http.ResponseWriter, params any) {
	if rpcServer.fileAnalyzer != nil {
		ctx := context.Background()
		if normalized, ok := params.(normalizedRPCParams); ok && normalized.ctx != nil {
			ctx = normalized.ctx
		}
		sendResultResponse(response, rpcServer.fileAnalyzer.Status(ctx, true, true))
		return
	}
	searchPath := settingString(rpcServer.settings, "ffmpeg_path")
	if dataDir := settingString(rpcServer.settings, "data_dir"); dataDir != "" {
		searchPath = appendSearchPath(searchPath, filepath.Join(dataDir, "ffmpeg", "bin"))
	}
	searchPath = appendSearchPath(searchPath, os.Getenv("PATH"))
	ffmpeg, ffprobe := lookPathIn("ffmpeg", searchPath), lookPathIn("ffprobe", searchPath)
	available := false
	if ffmpeg != "" && ffprobe != "" && executableVersionOK(ffprobe, "") && executableVersionOK(ffmpeg, "ffmpeg") {
		available = true
	}
	var which any
	if ffmpeg != "" {
		which = ffmpeg
	}
	analysisTime, _ := strconv.Atoi(fmt.Sprint(settingValue(rpcServer.settings, "volume_analysis_time")))
	sendResultResponse(response, map[string]any{
		"available": available, "which": which, "analyze_audio_volume": analysisTime > 0,
	})
}

func settingValue(settings SettingsStore, name string) any {
	value, _ := settings.Get(name)
	return value
}

func settingString(settings SettingsStore, name string) string {
	value := settingValue(settings, name)
	text, _ := value.(string)
	return text
}

func appendSearchPath(current, addition string) string {
	if current == "" {
		return addition
	}
	if addition == "" {
		return current
	}
	return current + string(os.PathListSeparator) + addition
}

func lookPathIn(name, searchPath string) string {
	for _, directory := range filepath.SplitList(searchPath) {
		if directory == "" {
			continue
		}
		candidate, err := exec.LookPath(filepath.Join(directory, name))
		if err == nil {
			return candidate
		}
	}
	return ""
}

func executableVersionOK(path, prefix string) bool {
	output, err := exec.Command(path, "-version").CombinedOutput()
	return err == nil && (prefix == "" || strings.HasPrefix(string(output), prefix))
}
