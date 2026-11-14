package settings

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// environment is everything the default directories depend on, so tests can supply a fake one.
type Environment struct {
	goos     string
	home     string
	getenv   func(string) string
	exists   func(path string) bool
	readFile func(path string) ([]byte, error)
}

func realEnvironment(goos string) Environment {
	home, _ := os.UserHomeDir()
	return Environment{
		goos:   goos,
		home:   home,
		getenv: os.Getenv,
		exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		readFile: os.ReadFile,
	}
}

var xdgDownload = regexp.MustCompile(`(?m)^XDG_DOWNLOAD_DIR=(.+)$`)

// defaultDirs returns the data, wallet and download directories the Python SDK would pick, so a
// machine that already runs lbrynet keeps using the same folders. LBRY_DATA_DIR, LBRY_WALLET_DIR and
// LBRY_DOWNLOAD_DIR override the defaults (the data and wallet directories cannot be changed once
// running, because the settings file lives inside the data directory).
func defaultDirs(env Environment) (dataDir, walletDir, downloadDir string) {
	switch env.goos {
	case "windows":
		appData := env.getenv("APPDATA")
		localAppData := env.getenv("LOCALAPPDATA")
		if localAppData == "" {
			localAppData = appData
		}
		downloadDir = filepath.Join(env.home, "Downloads")
		oldData, oldWallet := filepath.Join(appData, "lbrynet"), filepath.Join(appData, "lbryum")
		if env.exists(oldData) || env.exists(oldWallet) {
			dataDir, walletDir = oldData, oldWallet
		} else {
			dataDir = filepath.Join(localAppData, "lbry", "lbrynet")
			walletDir = filepath.Join(localAppData, "lbry", "lbryum")
		}
	case "darwin":
		dataDir = filepath.Join(env.home, "Library", "Application Support", "LBRY")
		walletDir = filepath.Join(env.home, ".lbryum")
		downloadDir = filepath.Join(env.home, "Downloads")
	default: // linux and the other unixes
		downloadDir = linuxDownloadDir(env)
		oldData, oldWallet := filepath.Join(env.home, ".lbrynet"), filepath.Join(env.home, ".lbryum")
		if env.exists(oldData) || env.exists(oldWallet) {
			dataDir, walletDir = oldData, oldWallet
		} else {
			share := env.getenv("XDG_DATA_HOME")
			if share == "" {
				share = filepath.Join(env.home, ".local", "share")
			}
			dataDir = filepath.Join(share, "lbry", "lbrynet")
			walletDir = filepath.Join(share, "lbry", "lbryum")
		}
	}

	if v := env.getenv("LBRY_DATA_DIR"); v != "" {
		dataDir = v
	}
	if v := env.getenv("LBRY_WALLET_DIR"); v != "" {
		walletDir = v
	}
	if v := env.getenv("LBRY_DOWNLOAD_DIR"); v != "" {
		downloadDir = v
	}
	return dataDir, walletDir, downloadDir
}

func linuxDownloadDir(env Environment) string {
	configHome := env.getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(env.home, ".config")
	}
	if data, err := env.readFile(filepath.Join(configHome, "user-dirs.dirs")); err == nil {
		if m := xdgDownload.FindSubmatch(data); m != nil {
			dir := strings.Trim(strings.TrimSpace(string(m[1])), `"`)
			if dir != "" {
				return strings.ReplaceAll(dir, "$HOME", env.home)
			}
		}
	} else if v := env.getenv("XDG_DOWNLOAD_DIR"); v != "" {
		return v
	}
	return filepath.Join(env.home, "Downloads")
}

// expandPath resolves ~ and $VARS in a path setting, like the SDK does when it reads one.
func expandPath(env Environment, p string) string {
	p = os.Expand(p, env.getenv)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = filepath.Join(env.home, p[1:])
	}
	return p
}
