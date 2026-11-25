# syntax=docker/dockerfile:1

# Set by scripts/check-upstream.sh
ARG RAILPACK_VERSION=0.43.0

# Dependencies
# https://hub.docker.com/_/alpine/tags?name=3.24.4
ARG BASE_IMAGE=docker.io/library/alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
ARG DISTRO=alpine-3.24
# https://github.com/pgsty/mc/releases
ARG MINIO_CLIENT_VERSION=RELEASE.2026-11-12T00-00-00Z
# https://hub.docker.com/r/pgsty/mc/tags
ARG MINIO_CLIENT_IMAGE=docker.io/pgsty/mc:${MINIO_CLIENT_VERSION}@sha256:b532278b7a3747dbab6fe45eebb40a653efa704e6758e24e9d9ec54fc96c1800
# https://hub.docker.com/_/golang/tags?name=1.27 — builds coolify-affected, scripts/test-affected.sh tests with the same image
ARG GO_IMAGE=docker.io/library/golang:1.27.4-alpine3.24@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280

# Set by the build workflow
ARG IMAGE_VERSION
ARG SOURCE_DATE_EPOCH
ARG CREATED
ARG GIT_SHA

FROM ${MINIO_CLIENT_IMAGE} AS minio-client

# coolify-affected is built from source on the build platform and cross-compiled, so it is static and the same for every platform build
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS affected

ARG TARGETOS
ARG TARGETARCH
ARG IMAGE_VERSION

WORKDIR /src
COPY affected/go.mod affected/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY affected/ ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" GOFLAGS=-buildvcs=false \
    go build -trimpath -ldflags "-s -w -X github.com/coollabsio/coolify-helper/src/affected/internal/cli.Version=${IMAGE_VERSION:-0.0.0-dev}" \
    -o /out/coolify-affected ./cmd/coolify-affected

FROM ${BASE_IMAGE} AS build

ARG TARGETPLATFORM
ARG MINIO_CLIENT_VERSION
ARG IMAGE_VERSION

# binutils, curl and jq are only used during the build stage, so they are not pinned to a specific version
# hadolint ignore=DL3018
RUN apk add --no-cache binutils curl jq

WORKDIR /src
COPY --chmod=0644 upstream.json verify.sh ./
RUN sh verify.sh

COPY --chmod=0644 rootfs.sh /src/
RUN sh /src/rootfs.sh
COPY --from=minio-client /usr/bin/mc /rootfs/usr/bin/mc
COPY --from=affected --chmod=0755 /out/coolify-affected /rootfs/usr/local/bin/coolify-affected

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
        "generic|mc|${MINIO_CLIENT_VERSION}|AGPL-3.0-or-later|minio:mc|/usr/bin/mc" \
        "generic|coolify-affected|${IMAGE_VERSION:-0.0.0-dev}|Apache-2.0|coollabsio:coolify-affected|/usr/local/bin/coolify-affected"

FROM ${BASE_IMAGE} AS final

ARG BASE_IMAGE
ARG DISTRO
ARG RAILPACK_VERSION
ARG IMAGE_VERSION
ARG CREATED
ARG GIT_SHA

# The packages follow the Alpine release of the base image, so they are not pinned to a specific version
# hadolint ignore=DL3017,DL3018
RUN apk upgrade --no-cache \
    && apk add --no-cache bash curl git git-lfs openssh-client pigz tar tini

COPY --from=build /rootfs/ /

LABEL org.opencontainers.image.title="Coolify Helper" \
    org.opencontainers.image.description="The tools Coolify runs on every server to build and deploy applications." \
    org.opencontainers.image.version="${IMAGE_VERSION}" \
    org.opencontainers.image.revision="${GIT_SHA}" \
    org.opencontainers.image.created="${CREATED}" \
    org.opencontainers.image.licenses="Apache-2.0" \
    org.opencontainers.image.authors="coolLabs" \
    org.opencontainers.image.vendor="coolLabs" \
    org.opencontainers.image.url="https://hub.docker.com/r/coollabsio/coolify-helper#readme" \
    org.opencontainers.image.documentation="https://github.com/coollabsio/coolify-helper#readme" \
    org.opencontainers.image.source="https://github.com/coollabsio/coolify-helper#readme" \
    org.opencontainers.image.base.name="${BASE_IMAGE%@*}" \
    org.opencontainers.image.base.digest="${BASE_IMAGE#*@}" \
    io.coolify.image.version="${IMAGE_VERSION}" \
    io.coolify.image.distro="${DISTRO}" \
    io.coolify.image.variant="runtime" \
    io.coolify.image.flavor="" \
    io.coolify.image.shell="bash,busybox" \
    io.coolify.image.package-manager="apk" \
    coolify.managed="true"

ENV RAILPACK_VERSION=${RAILPACK_VERSION}

WORKDIR /artifacts

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["tail", "-f", "/dev/null"]
