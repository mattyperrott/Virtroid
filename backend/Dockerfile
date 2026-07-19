ARG GO_BUILD_IMAGE=golang:1.26.5-bookworm
ARG BACKEND_RUNTIME_IMAGE=debian:bookworm-slim

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

FROM ${BACKEND_RUNTIME_IMAGE}

RUN apt-get update \
    && apt-get install -y --no-install-recommends adb ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --shell /bin/bash virtroid

COPY --from=build /out/virtroidd /usr/local/bin/virtroidd
COPY --from=build /out/virtnoded /usr/local/bin/virtnoded
COPY --from=build /out/virtroid-viewercrypt /usr/local/bin/virtroid-viewercrypt
COPY --from=build /out/falco-forwarder /usr/local/bin/falco-forwarder

USER virtroid
WORKDIR /home/virtroid

EXPOSE 8080 8090

CMD ["virtroidd"]
