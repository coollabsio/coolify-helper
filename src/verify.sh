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
		download "$tool"
		check_download "$tool"
		unpack "$tool"
		check_version "$tool"
	done
	echo "verify: $(jq -r '[to_entries[] | "\(.key) \(.value.latest.version)"] | join(", ")' upstream.json) for $TARGETPLATFORM, sizes, SHA-256 and versions match"
}

download() {
	curl --proto '=https' --tlsv1.2 -sSfL --retry 3 --retry-all-errors -o "$1" "$(pin "$1" url)"
}

check_download() {
	size="$(wc -c < "$1" | tr -d ' ')"
	[ "$size" = "$(pin "$1" size)" ] || die "$1 has $size bytes, expected $(pin "$1" size)"
	[ "$(sha256sum "$1" | cut -d ' ' -f 1)" = "$(pin "$1" sha256)" ] || die "SHA-256 of $1 does not match upstream.json"
}

unpack() {
	case "$1:$(pin "$1" url)" in
		docker:*) tar -xzf docker -O docker/docker > bin/docker ;;
		*.tgz | *.tar.gz) tar -xzf "$1" -O "$1" > "bin/$1" ;;
		*) cp "$1" "bin/$1" ;;
	esac
	chmod 0755 "bin/$1"
}

check_version() {
	version="$(jq -r --arg tool "$1" '.[$tool].latest.version' upstream.json)"
	if [ "$1" = buildx ]; then
		reported="$(bin/buildx version)"
	else
		reported="$("bin/$1" --version 2> /dev/null | head -n 1)"
	fi
	echo " $reported " | grep -qE "[ v]$(echo "$version" | sed 's/\./\\./g')[ ,+]" || die "$1 reports '$reported', expected version $version"
}

pin() {
	jq -r --arg tool "$1" --arg platform "$TARGETPLATFORM" ".[\$tool].latest.platforms[\$platform].$2" upstream.json
}

main
