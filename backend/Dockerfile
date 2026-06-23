# Step 1: Build (CGO-free, pure-Go pgx driver — no SQLite/libc needed)
FROM golang:1.23-bookworm AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o server .


# Step 2: Final (distroless static — includes CA certificates)
FROM gcr.io/distroless/static-debian12 AS final

WORKDIR /app

COPY --from=builder /app/server .

EXPOSE 8888
EXPOSE 8989
ENTRYPOINT ["./server"]
