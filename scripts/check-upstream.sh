#!/usr/bin/env bash
set -euo pipefail

OPTION="$*"
PLATFORMS='linux/amd64,linux/arm64'
NAMES='buildx=Docker Buildx,compose=Docker Compose,docker=Docker,mise=mise,nixpacks=Nixpacks,pack=pack,railpack=Railpack'
SIGNER_WORKFLOWS='buildx=docker/github-builder/.github/workflows/bake.yml,compose=docker/github-builder/.github/workflows/bake.yml,mise=jdx/mise/.github/workflows/release.yml'
IMMUTABLE_RELEASES='mise,railpack'
CHANGE_DIRECTORY=.changes
WORK_DIR="$(mktemp -d)"

BOLD=$'\033[1m' BLUE=$'\033[34m' GREEN=$'\033[32m' RED=$'\033[31m' RESET=$'\033[0m'

heading() { printf '\n  %s%s%s\n' "$BOLD" "$*" "$RESET"; }
info() { printf '  %sℹ%s %s\n' "$BLUE" "$RESET" "$*"; }
ok() { printf '  %s✔%s %s\n' "$GREEN" "$RESET" "$*"; }
die() {
	printf '  %s✖%s %s\n' "$RED" "$RESET" "$*" >&2
	exit 1
}

cd "$(dirname "$0")/.."
trap 'rm -rf "$WORK_DIR"' EXIT

main() {
	[[ "$OPTION" =~ ^[a-z]*$ && ",,$(tools)," == *",$OPTION,"* ]] || die "'$OPTION' is not an option, expected $(tools | sed 's/,/, /g') or none"
	for tool in ${OPTION:-$(tools | tr ',' ' ')}; do
		check_tool "$tool"
	done
}

check_tool() {
	tool="$1"
	pinned="$(pin version)"
	heading "Checking $(name)"
	read_newest_release
	if [ "$version" = "$pinned" ]; then
		ok "$tool $BOLD$version$RESET is pinned and the newest release $source lists"
	else
		info "$source lists $tool $BOLD$version$RESET, $pinned is pinned"
		download_files
		verify_digests
		verify_provenance
		verify_immutable_release
		write_change_file
		write_upstream_json
		update_dockerfile
	fi
	[ "$tool" != railpack ] || check_tool mise
}

read_newest_release() {
	repository="$(pin 'platforms["linux/amd64"].url' | sed -nE 's|^https://github\.com/([^/]+/[^/]+)/releases/download/.*|\1|p')"
	case "$tool" in
		docker)
			source=download.docker.com
			version="$(fetch - https://download.docker.com/linux/static/stable/x86_64/ | grep -oE 'docker-[0-9]+\.[0-9]+\.[0-9]+\.tgz' | sed -E 's/^docker-|\.tgz$//g' | sort -V | tail -n 1)"
			;;
		mise)
			source="Railpack $(pin version railpack)"
			version="$(fetch - "https://raw.githubusercontent.com/railwayapp/railpack/v$(pin version railpack)/core/mise/version.txt" | tr -d '[:space:]')"
			;;
		*)
			source="github.com/$repository"
			version="$(gh api "repos/$repository/releases/latest" --jq .tag_name)"
			version="${version#v}"
			;;
	esac
	[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the newest $tool release $source lists is '$version', expected major.minor.patch"
}

download_files() {
	local platform
	for platform in ${PLATFORMS//,/ }; do
		mkdir -p "$WORK_DIR/$platform"
		fetch "$WORK_DIR/$platform/$tool" "$(new_url "$platform")"
		ok "downloaded $(asset "$platform") for $platform"
	done
}

verify_digests() {
	local platform digest
	if [ -z "$repository" ]; then
		info "$source publishes no checksums or signatures, src/upstream.json pins the size and SHA-256 of the downloads"
		return
	fi
	release_tag="$(new_url linux/amd64 | cut -d / -f 8)"
	gh api "repos/$repository/releases/tags/$release_tag" > "$WORK_DIR/release.json"
	for platform in ${PLATFORMS//,/ }; do
		digest="$(jq -r --arg name "$(asset "$platform")" '.assets[] | select(.name == $name) | .digest // empty' "$WORK_DIR/release.json")"
		[ "$digest" = "sha256:$(sha256 "$platform")" ] || die "$(asset "$platform") does not match the digest GitHub lists for it, ${digest:-none}"
	done
	ok "the downloads match the SHA-256 digests GitHub lists for $repository $release_tag"
}

verify_provenance() {
	local workflow platform bundle
	workflow="$(lookup "$SIGNER_WORKFLOWS")"
	[ -n "$workflow" ] || return 0
	for platform in ${PLATFORMS//,/ }; do
		bundle=
		if jq -e --arg name "$(asset "$platform").sigstore.json" 'any(.assets[]; .name == $name)' "$WORK_DIR/release.json" > /dev/null; then
			bundle="$WORK_DIR/$platform/$tool.sigstore.json"
			fetch "$bundle" "$(new_url "$platform").sigstore.json"
		fi
		gh attestation verify "$WORK_DIR/$platform/$tool" ${bundle:+--bundle "$bundle"} --repo "$repository" --source-ref "refs/tags/$release_tag" \
			--signer-workflow "$workflow" --deny-self-hosted-runners > /dev/null 2>&1 \
			|| die "$(asset "$platform") has no valid Sigstore build provenance of $workflow for $repository $release_tag"
	done
	ok "the downloads carry the Sigstore build provenance of $BOLD$workflow$RESET at $release_tag"
}

verify_immutable_release() {
	local platform
	[[ ",$IMMUTABLE_RELEASES," == *",$tool,"* ]] || return 0
	for platform in ${PLATFORMS//,/ }; do
		gh release verify-asset "$release_tag" "$WORK_DIR/$platform/$tool" --repo "$repository" > /dev/null 2>&1 \
			|| die "$(asset "$platform") is not an asset of the immutable release $repository $release_tag"
	done
	ok "the downloads are assets of the immutable release $repository $release_tag"
}

write_change_file() {
	local file path from="$pinned"
	file="$CHANGE_DIRECTORY/$(name | tr '[:upper:] ' '[:lower:]-')-from-$pinned.md"
	for path in "$CHANGE_DIRECTORY"/*.md; do
		if grep -qx "$(name) from .* to $pinned" "$path" 2> /dev/null && ! grep -q '^version: ' "$path"; then
			file="$path"
			from="$(sed -n "s/^$(name) from \(.*\) to .*/\1/p" "$path")"
		fi
	done
	mkdir -p "$CHANGE_DIRECTORY"
	printf -- '---\ntype: changed\n---\n%s from %s to %s\n' "$(name)" "$from" "$version" > "$file"
	ok "$file describes $(name) from $from to $BOLD$version$RESET"
}

write_upstream_json() {
	local platform platforms='{}'
	for platform in ${PLATFORMS//,/ }; do
		platforms="$(jq -c --arg platform "$platform" --arg url "$(new_url "$platform")" --argjson size "$(wc -c < "$WORK_DIR/$platform/$tool")" --arg sha256 "$(sha256 "$platform")" \
			'.[$platform] = {$url, $size, $sha256}' <<< "$platforms")"
	done
	jq --tab --arg tool "$tool" --arg version "$version" --argjson platforms "$platforms" '.[$tool].latest = {$version, $platforms}' src/upstream.json > "$WORK_DIR/upstream.json"
	cp "$WORK_DIR/upstream.json" src/upstream.json
	ok "src/upstream.json pins $tool $BOLD$version$RESET with url, size and SHA-256 for ${PLATFORMS//,/ and }"
}

update_dockerfile() {
	[ "$tool" = railpack ] || return 0
	sed -E "s|^ARG RAILPACK_VERSION=.*|ARG RAILPACK_VERSION=$version|" src/Dockerfile > "$WORK_DIR/Dockerfile"
	cp "$WORK_DIR/Dockerfile" src/Dockerfile
	ok "src/Dockerfile sets RAILPACK_VERSION $BOLD$version$RESET"
}

tools() { jq -r '[keys[] | select(. != "mise")] | join(",")' src/upstream.json; }

pin() { jq -r --arg tool "${2:-$tool}" ".[\$tool].latest.$1" src/upstream.json; }

name() { lookup "$NAMES"; }

lookup() { tr ',' '\n' <<< "$1" | sed -n "s|^$tool=||p"; }

new_url() {
	local url
	url="$(pin "platforms[\"$1\"].url")"
	echo "${url//$pinned/$version}"
}

asset() { new_url "$1" | sed 's|.*/||'; }

sha256() { openssl dgst -sha256 -r "$WORK_DIR/$1/$tool" | cut -d ' ' -f 1; }

fetch() { curl --proto '=https' --tlsv1.2 -sSfL --retry 3 --retry-all-errors -o "$1" "$2"; }

main
