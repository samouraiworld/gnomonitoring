# Étape 1 : Build avec CGO
FROM golang:1.23.5 AS builder

WORKDIR /app


RUN apt-get update && apt-get install -y gcc libc-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .


RUN go build -o server .


FROM debian:bookworm-slim AS final

WORKDIR /app


RUN apt-get update && apt-get install -y libsqlite3-0 && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/server .
# COPY config.yaml ./config.yaml
