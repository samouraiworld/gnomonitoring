FROM golang:1.23.5 AS builder

WORKDIR /root
COPY go.mod go.sum block_exporter.go ./

RUN go mod tidy

