#!/bin/sh
set -eu

die() {
	echo "verify: $*" >&2
	exit 1
}

cd "$(dirname "$0")"

main() {
	mkdir -p bin
	for tool in $(jq -r 'keys[]' upstream.json); do
		check_download "$tool"
		unpack "$tool"
	done
	echo "verify: $(jq -r '[to_entries[] | "\(.key) \(.value.latest.version)"] | join(", ")' upstream.json) for $TARGETPLATFORM, sizes and SHA-256 match"
}

check_download() {
	size="$(wc -c < "$1" | tr -d ' ')"
	[ "$size" = "$(pin "$1" size)" ] || die "$1 has $size bytes, expected $(pin "$1" size)"
	[ "$(openssl dgst -sha256 -r "$1" | cut -d ' ' -f 1)" = "$(pin "$1" sha256)" ] || die "SHA-256 of $1 does not match upstream.json"
}

unpack() {
	case "$1:$(pin "$1" url)" in
		docker:*) tar -xzf docker -O docker/docker > bin/docker ;;
		*.tgz | *.tar.gz) tar -xzf "$1" -O "$1" > "bin/$1" ;;
		*) cp "$1" "bin/$1" ;;
	esac
	chmod 0755 "bin/$1"
}

pin() {
	jq -r --arg tool "$1" --arg platform "$TARGETPLATFORM" ".[\$tool].latest.platforms[\$platform].$2" upstream.json
}

main
