package wallet

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultDirectory returns the directory holding the wallet files, following
// lbry-sdk's lbry/conf.py (get_*_directories): <lbryum dir>/wallets.
// LBRY_WALLET_DIR overrides the whole result.
func DefaultDirectory() string {
	if dir := os.Getenv("LBRY_WALLET_DIR"); dir != "" {
		return dir
	}
	return defaultDirectory(runtime.GOOS, os.UserHomeDir, os.Getenv, isDirectory)
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func defaultDirectory(goos string, userHomeDir func() (string, error), getenv func(string) string, isDir func(string) bool) string {
	home, err := userHomeDir()
	if err != nil {
		return ""
	}
	switch goos {
	case "darwin":
		// get_darwin_directories: ~/.lbryum
		return filepath.Join(home, ".lbryum", "wallets")
	case "windows":
		// get_windows_directories: the old roaming location wins if it exists,
		// otherwise appdirs user_data_dir('lbryum', 'lbry') under local app data.
		roaming := getenv("APPDATA")
		if roaming == "" {
			roaming = filepath.Join(home, "AppData", "Roaming")
		}
		old := filepath.Join(roaming, "lbryum")
		if isDir(filepath.Join(roaming, "lbrynet")) || isDir(old) {
			return filepath.Join(old, "wallets")
		}
		local := getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(local, "lbry", "lbryum", "wallets")
	default:
		// get_linux_directories: ~/.lbryum if ~/.lbrynet or ~/.lbryum exists,
		// otherwise user_data_dir('lbry/lbryum') (XDG_DATA_HOME or ~/.local/share).
		old := filepath.Join(home, ".lbryum")
		if isDir(filepath.Join(home, ".lbrynet")) || isDir(old) {
			return filepath.Join(old, "wallets")
		}
		data := getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(data, "lbry", "lbryum", "wallets")
	}
}
