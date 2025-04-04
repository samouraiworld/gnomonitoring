FROM golang:1.22 as builder

WORKDIR /root
COPY go.mod go.sum block_exporter.go ./

RUN go mod tidy
# RUN go build -o block_exporter .

