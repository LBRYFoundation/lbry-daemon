package config

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

func DefaultPaths() Paths {
	home, _ := os.UserHomeDir()
	paths := Paths{}
	switch runtime.GOOS {
	case "darwin":
		paths.DataDir = filepath.Join(home, "Library", "Application Support", "LBRY")
		paths.WalletDir = filepath.Join(home, ".lbryum")
		paths.DownloadDir = filepath.Join(home, "Downloads")
	case "windows":
		paths = windowsPaths(home)
	case "linux":
		paths = linuxPaths(home)
	}
	if paths.DataDir != "" {
		paths.Config = filepath.Join(paths.DataDir, "daemon_settings.yml")
	}
	return paths
}

func linuxPaths(home string) Paths {
	downloadDir := filepath.Join(home, "Downloads")
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	userDirsPath := filepath.Join(configHome, "user-dirs.dirs")
	if data, err := os.ReadFile(userDirsPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			name, value, found := strings.Cut(line, "=")
			if found && name == "XDG_DOWNLOAD_DIR" {
				downloadDir = strings.Trim(value, "\"")
				downloadDir = strings.ReplaceAll(downloadDir, "$HOME", home)
				break
			}
		}
	} else if configured := os.Getenv("XDG_DOWNLOAD_DIR"); configured != "" {
		downloadDir = configured
	}

	legacyData := filepath.Join(home, ".lbrynet")
	legacyWallet := filepath.Join(home, ".lbryum")
	if isDirectory(legacyData) || isDirectory(legacyWallet) {
		return Paths{DataDir: legacyData, WalletDir: legacyWallet, DownloadDir: downloadDir}
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	return Paths{
		DataDir:     filepath.Join(dataHome, "lbry", "lbrynet"),
		WalletDir:   filepath.Join(dataHome, "lbry", "lbryum"),
		DownloadDir: downloadDir,
	}
}

func windowsPaths(home string) Paths {
	downloadDir := filepath.Join(home, "Downloads")
	roaming := os.Getenv("APPDATA")
	legacyData := filepath.Join(roaming, "lbrynet")
	legacyWallet := filepath.Join(roaming, "lbryum")
	if roaming != "" && (isDirectory(legacyData) || isDirectory(legacyWallet)) {
		return Paths{DataDir: legacyData, WalletDir: legacyWallet, DownloadDir: downloadDir}
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = roaming
	}
	return Paths{
		DataDir:     filepath.Join(local, "lbry", "lbrynet"),
		WalletDir:   filepath.Join(local, "lbry", "lbryum"),
		DownloadDir: downloadDir,
	}
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func ExpandPath(path string) string {
	path = expandEnvironment(path)
	if runtime.GOOS == "windows" {
		path = expandPercentEnvironment(path)
	}
	separators := "/"
	if runtime.GOOS == "windows" {
		separators = `/\`
	}
	if path == "~" || len(path) > 1 && path[0] == '~' && strings.ContainsRune(separators, rune(path[1])) {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return home + path[1:]
		}
	}
	if strings.HasPrefix(path, "~") {
		end := strings.IndexAny(path, separators)
		name := strings.TrimPrefix(path, "~")
		if end >= 0 {
			name = path[1:end]
		}
		if account, err := user.Lookup(name); err == nil {
			if end < 0 {
				return account.HomeDir
			}
			return account.HomeDir + path[end:]
		}
	}
	return path
}

func expandPercentEnvironment(path string) string {
	var expanded strings.Builder
	for index := 0; index < len(path); {
		if path[index] != '%' {
			expanded.WriteByte(path[index])
			index++
			continue
		}
		closing := strings.IndexByte(path[index+1:], '%')
		if closing < 0 {
			expanded.WriteString(path[index:])
			break
		}
		closing += index + 1
		name := path[index+1 : closing]
		if name == "" {
			expanded.WriteByte('%')
		} else if value, exists := os.LookupEnv(name); exists {
			expanded.WriteString(value)
		} else {
			expanded.WriteString(path[index : closing+1])
		}
		index = closing + 1
	}
	return expanded.String()
}

func expandEnvironment(path string) string {
	var expanded strings.Builder
	for index := 0; index < len(path); {
		if path[index] != '$' || index+1 >= len(path) {
			expanded.WriteByte(path[index])
			index++
			continue
		}

		start := index
		index++
		name := ""
		if path[index] == '{' {
			closing := strings.IndexByte(path[index+1:], '}')
			if closing < 0 {
				expanded.WriteString(path[start:])
				break
			}
			closing += index + 1
			name = path[index+1 : closing]
			index = closing + 1
		} else {
			nameStart := index
			for index < len(path) && isEnvironmentNameByte(path[index]) {
				index++
			}
			if index == nameStart {
				expanded.WriteByte('$')
				continue
			}
			name = path[nameStart:index]
		}

		if value, exists := os.LookupEnv(name); exists {
			expanded.WriteString(value)
		} else {
			expanded.WriteString(path[start:index])
		}
	}
	return expanded.String()
}

func isEnvironmentNameByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
