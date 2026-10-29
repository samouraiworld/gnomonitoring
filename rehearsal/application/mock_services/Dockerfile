FROM golang:1.26.10-bookworm AS builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY rehearsal/application/mock_services/main.go /src/mock/main.go
RUN CGO_ENABLED=0 go build -o /synthetic-mocks /src/mock/main.go

FROM python:3.13.12-slim-bookworm@sha256:a58daefb915e1e03ad48f3ca4df8832065412c5c35cacb9d39f4229184de12b6
RUN groupadd --gid 999 rehearsal && useradd --uid 999 --gid 999 --no-create-home rehearsal && mkdir -p /run/gnomon-test && chown 999:999 /run/gnomon-test
COPY --from=builder /synthetic-mocks /usr/local/bin/synthetic-mocks
USER 999:999
ENTRYPOINT ["/usr/local/bin/synthetic-mocks"]
