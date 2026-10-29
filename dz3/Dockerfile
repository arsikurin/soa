FROM public.ecr.aws/docker/library/golang:1.26-alpine3.23 AS build
RUN apk add --no-cache protobuf-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ ./api/
COPY proto/ ./proto/
COPY internal/ ./internal/
COPY cmd/ ./cmd/
RUN go generate ./... && CGO_ENABLED=0 go build -trimpath -o /out/booking ./cmd/booking && CGO_ENABLED=0 go build -trimpath -o /out/flight ./cmd/flight

FROM public.ecr.aws/docker/library/alpine:3.23
COPY --from=build /out/booking /out/flight /usr/local/bin/
USER 10001:10001
CMD ["booking"]
