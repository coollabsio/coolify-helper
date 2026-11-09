# syntax=docker/dockerfile:1

# https://github.com/railwayapp/railpack/releases, the version src/upstream.json pins
ARG RAILPACK_VERSION=0.43.0

# Build dependencies
# https://hub.docker.com/_/alpine/tags?name=3.24.3
ARG BASE_IMAGE=alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
# https://github.com/pgsty/mc/releases
ARG MINIO_CLIENT_VERSION=RELEASE.2026-11-09T00-00-00Z
# https://hub.docker.com/r/pgsty/mc/tags
ARG MINIO_CLIENT_IMAGE=docker.io/pgsty/mc:${MINIO_CLIENT_VERSION}@sha256:82b1c51de77aa7687d4d2c1a2b388dc7752528a64377c48a3e1271dcac39dbcf

# Set by the build workflow
ARG IMAGE_VERSION
ARG SOURCE_DATE_EPOCH
ARG CREATED
ARG GIT_SHA

FROM ${MINIO_CLIENT_IMAGE} AS minio-client

FROM ${BASE_IMAGE} AS build

ARG TARGETPLATFORM
ARG MINIO_CLIENT_VERSION

# binutils, curl, jq and openssl are only used during the build stage, so they are not pinned to a specific version
RUN apk add --no-cache binutils curl jq openssl

WORKDIR /src
COPY --chmod=0644 upstream.json verify.sh ./
RUN for tool in $(jq -r 'keys[]' upstream.json); do \
        curl --proto '=https' --tlsv1.2 -sSfL --retry 3 --retry-all-errors -o "$tool" "$(jq -r --arg tool "$tool" --arg platform "$TARGETPLATFORM" '.[$tool].latest.platforms[$platform].url' upstream.json)" || exit 1; \
    done \
    && sh verify.sh

# Assemble the files the final image adds to the base image in /rootfs
COPY --chmod=0644 rootfs.sh /src/
RUN sh /src/rootfs.sh
COPY --from=minio-client /usr/bin/mc /rootfs/usr/bin/mc

# The tools have no package database for the SBOM scanner to read, sbom.sh declares each in its binary
COPY --chmod=0644 sbom.sh /src/
RUN version() { jq -r --arg tool "$1" '.[$tool].latest.version' upstream.json; } \
    && sh /src/sbom.sh coollabsio/coolify-helper "Coolify Helper" \
        "generic|docker|$(version docker)|Apache-2.0|docker:docker|/usr/bin/docker" \
        "generic|compose|$(version compose)|Apache-2.0|docker:compose|/root/.docker/cli-plugins/docker-compose" \
        "generic|buildx|$(version buildx)|Apache-2.0|docker:buildx|/root/.docker/cli-plugins/docker-buildx" \
        "generic|pack|$(version pack)|Apache-2.0|buildpacks:pack|/usr/local/bin/pack" \
        "generic|nixpacks|$(version nixpacks)|MIT|railway:nixpacks|/usr/local/bin/nixpacks" \
        "generic|railpack|$(version railpack)|MIT|railway:railpack|/usr/local/bin/railpack" \
        "generic|mise|$(version mise)|MIT|jdx:mise|/tmp/railpack/mise/mise-$(version mise)" \
        "generic|mc|${MINIO_CLIENT_VERSION}|AGPL-3.0-or-later|minio:mc|/usr/bin/mc"

FROM ${BASE_IMAGE} AS final

ARG BASE_IMAGE
ARG RAILPACK_VERSION
ARG IMAGE_VERSION
ARG CREATED
ARG GIT_SHA

# The packages follow the Alpine release of the base image, so they are not pinned to a specific version
RUN apk upgrade --no-cache \
    && apk add --no-cache bash curl git git-lfs openssh-client pigz tar tini

COPY --from=build /rootfs/ /

# https://github.com/opencontainers/image-spec/blob/main/annotations.md#pre-defined-annotation-keys
LABEL org.opencontainers.image.title="Coolify Helper" \
    org.opencontainers.image.description="The tools Coolify runs on every server to build and deploy applications." \
    org.opencontainers.image.version="${IMAGE_VERSION}" \
    org.opencontainers.image.created="${CREATED}" \
    org.opencontainers.image.revision="${GIT_SHA}" \
    org.opencontainers.image.licenses="Apache-2.0" \
    org.opencontainers.image.authors="coollabsio" \
    org.opencontainers.image.vendor="coollabsio" \
    org.opencontainers.image.url="https://hub.docker.com/r/coollabsio/coolify-helper" \
    org.opencontainers.image.source="https://github.com/coollabsio/coolify-helper" \
    org.opencontainers.image.documentation="https://github.com/coollabsio/coolify-helper#readme" \
    org.opencontainers.image.base.name="${BASE_IMAGE}" \
    coolify.managed="true"

ENV RAILPACK_VERSION=${RAILPACK_VERSION}

WORKDIR /artifacts

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["tail", "-f", "/dev/null"]
