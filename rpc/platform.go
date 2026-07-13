package rpc

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const legacySDKVersion = "0.113.0"

var buildType = "dev"

func collectPlatformInfo() map[string]any {
	osSystem := map[string]string{
		"android": "android",
		"darwin":  "Darwin",
		"linux":   "Linux",
		"windows": "Windows",
	}[runtime.GOOS]
	if osSystem == "" {
		osSystem = runtime.GOOS
	}
	if _, android := os.LookupEnv("ANDROID_ARGUMENT"); android {
		osSystem = "android"
	}
	release := kernelRelease()
	info := map[string]any{
		"build":           buildType,
		"lbrynet_version": legacySDKVersion,
		"os_release":      release,
		"os_system":       osSystem,
		"platform":        osSystem + "-" + release + "-" + runtime.GOARCH + "-with-" + runtime.Version(),
		"processor":       runtime.GOARCH,
		"python_version":  runtime.Version(),
		"version":         legacySDKVersion,
	}
	if osSystem == "Linux" {
		info["desktop"] = environmentValue("XDG_CURRENT_DESKTOP", "Unknown")
		info["distro"] = linuxDistroInfo()
	}
	return info
}

func kernelRelease() string {
	if runtime.GOOS == "linux" {
		if release, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			if trimmed := strings.TrimSpace(string(release)); trimmed != "" {
				return trimmed
			}
		}
	}
	return runtime.GOOS
}

func environmentValue(name, fallback string) string {
	if value, exists := os.LookupEnv(name); exists {
		return value
	}
	return fallback
}

func linuxDistroInfo() map[string]any {
	values := readOSRelease()
	versionParts := strings.Split(values["VERSION_ID"], ".")
	parts := map[string]string{
		"major":        "",
		"minor":        "",
		"build_number": "",
	}
	for index, name := range []string{"major", "minor", "build_number"} {
		if index < len(versionParts) {
			parts[name] = versionParts[index]
		}
	}
	return map[string]any{
		"codename":      values["VERSION_CODENAME"],
		"id":            values["ID"],
		"like":          values["ID_LIKE"],
		"version":       values["VERSION_ID"],
		"version_parts": parts,
	}
}

func readOSRelease() map[string]string {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return map[string]string{}
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), "=")
		if !found {
			continue
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		values[name] = value
	}
	return values
}
