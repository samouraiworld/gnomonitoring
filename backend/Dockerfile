# Étape 1 : Build avec CGO
FROM golang:1.23.5 AS builder

WORKDIR /app

# Installer les dépendances C pour sqlite
RUN apt-get update && apt-get install -y gcc libc-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Ici CGO_ENABLED=1 (par défaut) et GOOS=linux
RUN go build -o server .

# Étape 2 : Image finale
FROM debian:bookworm-slim AS final

WORKDIR /app

# Installer la lib C minimale pour sqlite
RUN apt-get update && apt-get install -y libsqlite3-0 && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/server .
COPY config_docker.yaml ./config.yaml
