FROM golang:1.26-alpine3.23 AS build
RUN apk add --no-cache protobuf-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ ./api/
COPY proto/ ./proto/
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN go generate ./... \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/marketplace ./cmd/marketplace

FROM alpine:3.23
COPY --from=build /out/marketplace /usr/local/bin/marketplace
USER 10001:10001
EXPOSE 8080 8081
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=5 \
    CMD ["/usr/local/bin/marketplace", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/marketplace"]
