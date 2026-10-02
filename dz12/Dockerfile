FROM golang:1.26-alpine3.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/catalog ./cmd/catalog

FROM alpine:3.23
COPY --from=build /out/catalog /usr/local/bin/catalog
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=2s --start-period=3s --retries=3 \
    CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/health"]
ENTRYPOINT ["/usr/local/bin/catalog"]
