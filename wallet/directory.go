package wallet

import "os"
import "path/filepath"
import "runtime"

func GetDefaultWalletDirectory() string {
	switch runtime.GOOS {
	case "darwin":
		return GetDefaultWalletDirectoryForDarwin()
	case "windows":
		return GetDefaultWalletDirectoryForWindows()
	default:
		return GetDefaultWalletDirectoryForOther()
	}
}

func GetDefaultWalletDirectoryForDarwin() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// get_darwin_directories: ~/.lbryum
	return filepath.Join(home, ".lbryum", "wallets")
}
func GetDefaultWalletDirectoryForOther() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// get_linux_directories: ~/.lbryum if ~/.lbrynet or ~/.lbryum exists,
	// otherwise user_data_dir('lbry/lbryum') (XDG_DATA_HOME or ~/.local/share).
	old := filepath.Join(home, ".lbryum")
	if isDirectory(filepath.Join(home, ".lbrynet")) || isDirectory(old) {
		return filepath.Join(old, "wallets")
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "lbry", "lbryum", "wallets")
}

func GetDefaultWalletDirectoryForWindows() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// get_windows_directories: the old roaming location wins if it exists,
	// otherwise appdirs user_data_dir('lbryum', 'lbry') under local app data.
	roaming := os.Getenv("APPDATA")
	if roaming == "" {
		roaming = filepath.Join(home, "AppData", "Roaming")
	}
	old := filepath.Join(roaming, "lbryum")
	if isDirectory(filepath.Join(roaming, "lbrynet")) || isDirectory(old) {
		return filepath.Join(old, "wallets")
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(local, "lbry", "lbryum", "wallets")
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
