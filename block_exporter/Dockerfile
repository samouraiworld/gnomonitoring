FROM golang:1.23.4 AS builder

WORKDIR /root
COPY go.mod go.sum block_exporter.go ./

RUN go mod tidy
# RUN go build -o block_exporter .

