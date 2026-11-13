#!/usr/bin/env bash
set -euo pipefail

OPTION="$*"
IMAGE="${1:-}"
PROJECT=coolify-helper-test

BOLD=$'\033[1m' GREEN=$'\033[32m' RED=$'\033[31m' RESET=$'\033[0m'

heading() { printf '\n  %s%s%s\n' "$BOLD" "$*" "$RESET"; }
ok() { printf '  %s✔%s %s\n' "$GREEN" "$RESET" "$*"; }
die() {
	printf '  %s✖%s %s\n' "$RED" "$RESET" "$*" >&2
	exit 1
}

cd "$(dirname "$0")/.."
trap cleanup EXIT

main() {
	[[ "$OPTION" =~ ^[^-][^[:space:]]*$ ]] || die "'$OPTION' is not an option, expected an image"
	heading "Testing $IMAGE"
	check_versions
	check_railpack
	check_tools
	heading "Docker"
	check_daemon
	check_compose
	heading "All tests passed for $IMAGE"
}

check_versions() {
	check_version docker docker --version
	check_version compose docker compose version
	check_version buildx docker buildx version
	check_version pack pack --version
	check_version nixpacks nixpacks --version
	check_version railpack railpack --version
	check_version mise "/tmp/railpack/mise/mise-$(pinned mise)" --version
	ok "docker, compose, buildx, pack, nixpacks, railpack and mise print the versions src/upstream.json pins"
}

check_railpack() {
	local version
	version="$(helper printenv RAILPACK_VERSION)"
	[ "$version" = "$(pinned railpack)" ] || die "RAILPACK_VERSION is '$version', expected $(pinned railpack)"
	ok "RAILPACK_VERSION is $(pinned railpack), so the Railpack frontend matches the Railpack CLI"
}

check_tools() {
	helper which bash curl git git-lfs ssh pigz tar > /dev/null || die "bash, curl, git, git-lfs, ssh, pigz or tar is not installed"
	helper mc --version > /dev/null || die "mc --version failed"
	[ "$(helper ps -o comm | sed -n 2p)" = tini ] || die "PID 1 is not tini"
	ok "bash, curl, git, git-lfs, ssh, pigz, tar and mc are installed, tini runs as PID 1"
}

check_daemon() {
	local server
	server="$(helper_with_socket docker version --format '{{.Server.Version}}')"
	[ -n "$server" ] || die "docker version prints no server version"
	ok "the Docker CLI reaches Docker Engine $server through the mounted socket"
}

check_compose() {
	compose up -d --wait > /dev/null 2>&1 || die "docker compose up failed"
	[ "$(docker ps -q --filter "label=com.docker.compose.project=$PROJECT" | wc -l | tr -d ' ')" = 1 ] || die "docker compose up started no container"
	compose down > /dev/null 2>&1 || die "docker compose down failed"
	[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$PROJECT")" ] || die "docker compose down left containers behind"
	ok "docker compose starts and removes a project through the mounted socket"
}

check_version() {
	local tool="$1" version printed
	shift
	version="$(pinned "$tool")"
	printed="$(helper "$@")"
	[[ "$printed" =~ (^|[^0-9.])v?${version//./\\.}([^0-9.]|$) ]] || die "$* prints '$printed', expected $tool $version"
}

compose() {
	printf 'services:\n  app:\n    image: %s\n    command: [sleep, "60"]\n' "$IMAGE" \
		| helper_with_socket docker compose -p "$PROJECT" -f - "$@"
}

helper() {
	docker run --rm "$IMAGE" "$@"
}

helper_with_socket() {
	docker run --rm -i -v /var/run/docker.sock:/var/run/docker.sock "$IMAGE" "$@"
}

pinned() {
	jq -r --arg tool "$1" '.[$tool].latest.version' src/upstream.json
}

cleanup() {
	compose down > /dev/null 2>&1 || true
}

main
