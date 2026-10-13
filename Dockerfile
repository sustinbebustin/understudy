# Two targets share one runtime filesystem:
#   - the default (last) target compiles the binary from source, for
#     `just image` and the distribution tests;
#   - `release` copies the binary GoReleaser already built and checksummed,
#     so the image ships exactly the archived binary.

# The final image has no shell, so its root filesystem is staged here:
# CA certificates for image pulls over TLS, /data (owned by 65532) for
# persistence, a sticky /tmp for the blob store's spill files, and the
# licence and notices Apache-2.0 requires a redistribution to carry. Copying
# the staged root as one directory keeps each entry's mode and owner.
#
# The notices' modes are set in RUN rather than by COPY --chmod, which would
# also give the directories it creates 0644 and so make them untraversable.
#
# Both golang stages pin the multi-arch index by digest: the shipped CA
# bundle comes from it, so a re-pushed tag must not change the image.
FROM --platform=$BUILDPLATFORM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS rootfs
COPY LICENSE NOTICE /stage/usr/share/doc/understudy/
RUN mkdir -p /stage/etc/ssl/certs /stage/data /stage/tmp \
    && cp /etc/ssl/certs/ca-certificates.crt /stage/etc/ssl/certs/ \
    && chmod 0644 /stage/usr/share/doc/understudy/LICENSE /stage/usr/share/doc/understudy/NOTICE \
    && chown 65532:65532 /stage/data \
    && chmod 1777 /stage/tmp

# The builder runs on the build machine and cross-compiles for the target, so
# a multi-arch build never emulates the Go toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS builder

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/understudy ./cmd/understudy

FROM scratch AS base

ARG VERSION=dev

LABEL org.opencontainers.image.title="understudy" \
      org.opencontainers.image.description="A local AWS emulator for development machines and CI" \
      org.opencontainers.image.source="https://github.com/sustinbebustin/understudy" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=rootfs /stage/ /

USER 65532:65532
VOLUME /data
EXPOSE 4566

ENTRYPOINT ["/understudy"]
CMD ["serve"]

# No --endpoint: health resolves it as every CLI command does, from
# UNDERSTUDY_ENDPOINT, else the port of UNDERSTUDY_LISTEN on loopback, else
# http://127.0.0.1:4566. A container started on another port with
# UNDERSTUDY_LISTEN stays healthy without a second variable.
HEALTHCHECK --interval=5s --timeout=2s --start-period=3s \
    CMD ["/understudy", "health"]

# GoReleaser's image: its build context holds the prebuilt binary of each
# platform under <os>/<arch>/.
FROM base AS release
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/understudy /understudy

# The default image, built from source.
FROM base
COPY --from=builder /out/understudy /understudy
