FROM golang:1.25 AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtroidd ./cmd/virtroidd
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtnoded ./cmd/virtnoded
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtroid-viewercrypt ./cmd/viewercrypt

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends adb ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --shell /bin/bash virtroid

COPY --from=build /out/virtroidd /usr/local/bin/virtroidd
COPY --from=build /out/virtnoded /usr/local/bin/virtnoded
COPY --from=build /out/virtroid-viewercrypt /usr/local/bin/virtroid-viewercrypt

USER virtroid
WORKDIR /home/virtroid

EXPOSE 8080 8090

CMD ["virtroidd"]
