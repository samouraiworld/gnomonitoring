FROM golang:1.22 as builder

WORKDIR /app
COPY . .

RUN go mod tidy
RUN go build -o block_exporter .

# Image finale
FROM alpine:latest
WORKDIR /root/
COPY --from=builder /app/block_exporter .

CMD ["./block_exporter"]