FROM golang:1.25 AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtdroidd ./cmd/virtdroidd
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtnoded ./cmd/virtnoded
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/virtdroid-viewercrypt ./cmd/viewercrypt

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends adb ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --shell /bin/bash virtdroid

COPY --from=build /out/virtdroidd /usr/local/bin/virtdroidd
COPY --from=build /out/virtnoded /usr/local/bin/virtnoded
COPY --from=build /out/virtdroid-viewercrypt /usr/local/bin/virtdroid-viewercrypt

USER virtdroid
WORKDIR /home/virtdroid

EXPOSE 8080 8090

CMD ["virtdroidd"]
