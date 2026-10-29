FROM golang:1.26.10 AS builder

WORKDIR /root
COPY go.mod go.sum block_exporter.go ./

RUN go mod tidy

