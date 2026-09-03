ARG GO_BUILD_IMAGE=golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36
ARG BACKEND_RUNTIME_IMAGE=alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
ARG SCHEMA_VERSION=unknown
ARG DEPLOYMENT_TREE_DIGEST=unknown

FROM ${GO_BUILD_IMAGE} AS build

WORKDIR /src
RUN go env GOVERSION | awk '{ version = substr($0, 3); split(version, parts, "."); if (parts[1] > 1 || (parts[1] == 1 && (parts[2] > 26 || (parts[2] == 26 && parts[3] >= 5)))) exit 0; exit 1 }'
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtroidd ./cmd/virtroidd
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtnoded ./cmd/virtnoded
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtroid-viewercrypt ./cmd/viewercrypt
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/falco-forwarder ./cmd/falco-forwarder
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtroid-admin ./cmd/virtroid-admin

FROM ${BACKEND_RUNTIME_IMAGE}

ARG VCS_REF
ARG BUILD_DATE
ARG SCHEMA_VERSION
ARG DEPLOYMENT_TREE_DIGEST

LABEL org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.version="sha-${VCS_REF}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      com.virtroid.schema-version="${SCHEMA_VERSION}" \
      com.virtroid.deployment-tree-sha256="${DEPLOYMENT_TREE_DIGEST}"

RUN apk add --no-cache \
      android-tools=35.0.2-r23 \
      ca-certificates=20260611-r0 \
    && adduser -D -h /home/virtroid -s /sbin/nologin virtroid

COPY --from=build /out/virtroidd /usr/local/bin/virtroidd
COPY --from=build /out/virtnoded /usr/local/bin/virtnoded
COPY --from=build /out/virtroid-viewercrypt /usr/local/bin/virtroid-viewercrypt
COPY --from=build /out/falco-forwarder /usr/local/bin/falco-forwarder
COPY --from=build /out/virtroid-admin /usr/local/bin/virtroid-admin

USER virtroid
WORKDIR /home/virtroid

EXPOSE 8080 8090

ENV VIRTROID_HEALTHCHECK_URL=http://127.0.0.1:8080/healthz
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -T 4 -O /dev/null "${VIRTROID_HEALTHCHECK_URL}" || exit 1

CMD ["virtroidd"]
