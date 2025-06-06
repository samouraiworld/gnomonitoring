FROM golang:1.23.5 as builder

WORKDIR /root
COPY go.mod go.sum moniGnoland.go .

RUN go mod tidy
# RUN go build -o block_exporter .

