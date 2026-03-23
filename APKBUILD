makedepends="go"
license="MIT"
arch="all"
url=https://lbry.org
pkgdesc="The LBRY Daemon."
pkgver=0.0.0
pkgrel=0
pkgname=lbryd

_commit=68db80d89bb263d1a4eccab2c8a86067e2b34336

source="$pkgname-$pkgver.tar.gz::https://github.com/LBRYFoundation/lbry-daemon/archive/$_commit.tar.gz"

options="net"
builddir="$srcdir/lbry-daemon-$_commit"
maintainer="LBRY Foundation <board@lbry.org>"

build() {
	go build -o lbryd
}

check() {
	go test ./...
}

package() {
	install -Dm755 "$builddir/lbryd" "$pkgdir/usr/bin/lbryd"
}

sha512sums="
d802b8745a6813eed86f9289d7dd37265357b1a11ba97bcbac0569163b06b6242beeee43c70a70a84cde301fc7adb696951139cb38ea18b1e11205093391840c  lbry-daemon-0.0.0.tar.gz
"
