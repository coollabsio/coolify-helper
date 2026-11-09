# syntax=docker/dockerfile:1

# https://github.com/railwayapp/railpack/releases, the version src/upstream.json pins
ARG RAILPACK_VERSION=0.43.0

# Build dependencies
# https://hub.docker.com/_/alpine/tags?name=3.24.3
ARG BASE_IMAGE=alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
# https://github.com/pgsty/mc/releases
ARG MINIO_CLIENT_IMAGE=docker.io/pgsty/mc:RELEASE.2026-11-09T00-00-00Z@sha256:82b1c51de77aa7687d4d2c1a2b388dc7752528a64377c48a3e1271dcac39dbcf

# Set by the build workflow
ARG IMAGE_VERSION
ARG SOURCE_DATE_EPOCH
ARG CREATED
ARG GIT_SHA

FROM ${MINIO_CLIENT_IMAGE} AS minio-client

FROM ${BASE_IMAGE} AS build

ARG TARGETPLATFORM

# curl, jq and openssl are only used during the build stage, so they are not pinned to a specific version
RUN apk add --no-cache curl jq openssl

WORKDIR /src
COPY --chmod=0644 upstream.json verify.sh ./
RUN for tool in $(jq -r 'keys[]' upstream.json); do \
        curl --proto '=https' --tlsv1.2 -sSfL --retry 3 --retry-all-errors -o "$tool" "$(jq -r --arg tool "$tool" --arg platform "$TARGETPLATFORM" '.[$tool].latest.platforms[$platform].url' upstream.json)" || exit 1; \
    done \
    && sh verify.sh

# Assemble the files the final image adds to the base image in /rootfs
COPY --chmod=0644 rootfs.sh /src/
RUN sh /src/rootfs.sh

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
COPY --from=minio-client /usr/bin/mc /usr/bin/mc

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
