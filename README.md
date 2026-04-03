# LBRY Daemon

[![Go](https://github.com/LBRYFoundation/lbry-daemon/actions/workflows/go.yml/badge.svg)](https://github.com/LBRYFoundation/lbry-daemon/actions/workflows/go.yml)
[![Docker Image CI](https://github.com/LBRYFoundation/lbry-daemon/actions/workflows/docker-image.yml/badge.svg)](https://github.com/LBRYFoundation/lbry-daemon/actions/workflows/docker-image.yml)
[![Codecov](https://codecov.io/gh/LBRYFoundation/lbry-daemon/graph/badge.svg)](https://codecov.io/gh/LBRYFoundation/lbry-daemon)
![Snapcraft Version](https://img.shields.io/snapcraft/v/lbry-daemon/latest/stable?logo=snapcraft)
![WinGet Package Version](https://img.shields.io/winget/v/LBRY.Daemon)
![Flathub Version](https://img.shields.io/flathub/v/org.lbry.daemon?logo=flathub)
![Homebrew Formula Version](https://img.shields.io/homebrew/v/lbry-daemon?logo=homebrew)
![Chocolatey Version](https://img.shields.io/chocolatey/v/lbry-daemon?logo=chocolatey)
![Scoop Version](https://img.shields.io/scoop/v/lbry-daemon)
![Conda Version](https://img.shields.io/conda/vn/conda-forge/lbry-daemon?logo=anaconda)

Native: [![Packaging status](https://repology.org/badge/vertical-allrepos/lbry-daemon.svg)](https://repology.org/project/lbry-daemon/versions)

The LBRY Daemon.

## Build

```shell
go build -o lbryd
```

Or for Windows:

```shell
go build -o lbryd.exe
```

## Install

### Snap

```shell
snap install lbryd
```

### WinGet

```shell
winget install LBRY.Daemon
```

### Flatpak (using Flathub)

```shell
flatpak install flathub org.lbry.daemon
```

## Run

```shell
lbryd
```

## License
This project is MIT licensed. For the full license, see [LICENSE](LICENSE.md).
