#!/bin/sh
# Builds the bdb command line for a release into dist/bdb, with the version stamped and the web app embedded, and
# makes sure it needs no shared library beyond the operating system's own, so it starts on any machine of its
# platform. The web app must be built and copied into pkg/webui first (make web-build webui-sync).
#
# On macOS the build is native: SQLCipher's crypto comes from the system's Security framework. On Linux it runs in an
# Alpine container and links everything statically, musl and OpenSSL's libcrypto included.
#
# Usage: scripts/build-cli-release.sh <version>
set -eu

version=${1:?usage: scripts/build-cli-release.sh <version>}
ldflags="-s -w -X github.com/alexandremgo/baguettedb/pkg/cli.version=${version}"
mkdir -p dist

case "$(uname -s)" in
Darwin)
	# The oldest macOS Go itself supports, so the C parts don't ask for a newer one than the runner's default would.
	CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=12.0 go build -trimpath -ldflags "${ldflags}" -o dist/bdb ./cmd/bdb
	if otool -L dist/bdb | tail -n +2 | grep -v -e '^[[:space:]]*/usr/lib/' -e '^[[:space:]]*/System/Library/'; then
		echo "dist/bdb links the libraries above, which not every Mac has" >&2
		exit 1
	fi
	;;
Linux)
	docker run --rm -v "${PWD}:/src" -w /src -e GOFLAGS=-buildvcs=false golang:1.26-alpine sh -ec "
		apk add --no-cache build-base openssl-dev openssl-libs-static pkgconf >/dev/null
		CGO_ENABLED=1 go build -trimpath -ldflags '${ldflags} -linkmode external -extldflags -static' \
			-o dist/bdb ./cmd/bdb"
	if ! file dist/bdb | grep -q 'statically linked'; then
		file dist/bdb >&2
		echo "dist/bdb is not statically linked" >&2
		exit 1
	fi
	;;
*)
	echo "release builds run on macOS and Linux only" >&2
	exit 1
	;;
esac
echo "Built dist/bdb ${version}"
