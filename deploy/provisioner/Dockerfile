# The runtime VM provisioner: virtroid-provisioner plus Aleph's official
# `aleph` CLI, built from a pinned source commit. Debian-based because the CLI
# links OpenSSL, D-Bus and libusb dynamically.
ARG RUST_BUILD_IMAGE=rust:1.96-bookworm@sha256:a339861ae23e9abb272cea45dfafde21760d2ce6577a70f8a926153677902663
ARG GO_BUILD_IMAGE=golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36
ARG RUNTIME_IMAGE=debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
# aleph-rs v0.21.0
ARG ALEPH_RS_COMMIT=748b3dd61654a02f2033226b5c5abba36c1ac0b0
ARG VCS_REF=unknown

FROM ${RUST_BUILD_IMAGE} AS aleph-cli
ARG ALEPH_RS_COMMIT
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        git pkg-config protobuf-compiler libssl-dev libdbus-1-dev libusb-1.0-0-dev libudev-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src/aleph-rs
RUN git init -q . \
    && git fetch -q --depth 1 https://github.com/aleph-im/aleph-rs.git "${ALEPH_RS_COMMIT}" \
    && git checkout -q FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = "${ALEPH_RS_COMMIT}"
RUN cargo build --release --locked --package aleph-cli \
    && install -D -m 0755 target/release/aleph /out/aleph

FROM ${GO_BUILD_IMAGE} AS provisioner
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
        -o /out/virtroid-provisioner ./cmd/virtroid-provisioner

FROM ${RUNTIME_IMAGE}
ARG VCS_REF
ARG ALEPH_RS_COMMIT
LABEL org.opencontainers.image.revision="${VCS_REF}" \
      com.virtroid.aleph-rs-commit="${ALEPH_RS_COMMIT}"
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libssl3 libdbus-1-3 libusb-1.0-0 libudev1 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10071 --home-dir /var/lib/virtroid-provisioner --shell /usr/sbin/nologin provisioner \
    && install -d -o provisioner -g provisioner -m 0700 /var/lib/virtroid-provisioner
COPY --from=aleph-cli /out/aleph /usr/local/bin/aleph
COPY --from=provisioner /out/virtroid-provisioner /usr/local/bin/virtroid-provisioner
# Fail the build rather than ship a CLI with an unresolved library.
RUN ! ldd /usr/local/bin/aleph | grep -q 'not found' && /usr/local/bin/aleph --version
USER provisioner
WORKDIR /var/lib/virtroid-provisioner
ENTRYPOINT ["/usr/local/bin/virtroid-provisioner"]
