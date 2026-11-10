# Step 1: Build (CGO-free, pure-Go pgx driver — no SQLite/libc needed)
FROM golang:1.26.10-bookworm AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o server . \
    && CGO_ENABLED=0 go build -ldflags="-s -w" -o migrate-user-ids ./cmd/migrate-user-ids


# Step 2: Final (distroless static — includes CA certificates)
FROM gcr.io/distroless/static-debian12 AS final

WORKDIR /app

COPY --from=builder /app/server .
# One-shot Clerk-to-Keycloak user id migration (run with --entrypoint).
COPY --from=builder /app/migrate-user-ids .

EXPOSE 8888
EXPOSE 8989
ENTRYPOINT ["./server"]
