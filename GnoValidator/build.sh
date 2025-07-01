#!/bin/sh 
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o main
docker build --network=host -t moni-gnoland .
