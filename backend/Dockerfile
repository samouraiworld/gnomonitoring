# Étape 1 : Build avec CGO
#FROM golang:1.26.1 AS builder
FROM golang:1.26.1-bookworm AS builder
WORKDIR /app


RUN apt-get update && apt-get install -y gcc libc-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .


RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o server .


FROM debian:bookworm-slim AS final

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    libsqlite3-0 \
    ca-certificates \
  && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/server .
# COPY config.yaml ./config.yaml

EXPOSE 8989
ENTRYPOINT ["./server"]
