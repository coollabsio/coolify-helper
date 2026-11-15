#!/bin/sh
set -eu

PLUGINS=/rootfs/root/.docker/cli-plugins

main() {
	create_directories
	install_binaries
	echo "rootfs: docker, compose, buildx, pack, nixpacks, railpack and mise installed"
}

create_directories() {
	install -d -m 0700 /rootfs/root
	install -d -m 1777 /rootfs/tmp
}

install_binaries() {
	install -D -m 0755 /src/bin/docker /rootfs/usr/bin/docker
	install -D -m 0755 /src/bin/compose "$PLUGINS/docker-compose"
	install -D -m 0755 /src/bin/buildx "$PLUGINS/docker-buildx"
	install -D -m 0755 /src/bin/pack /rootfs/usr/local/bin/pack
	install -D -m 0755 /src/bin/nixpacks /rootfs/usr/local/bin/nixpacks
	install -D -m 0755 /src/bin/railpack /rootfs/usr/local/bin/railpack
	install -D -m 0755 /src/bin/mise "/rootfs/tmp/railpack/mise/mise-$(jq -r .mise.latest.version /src/upstream.json)"
}

main
