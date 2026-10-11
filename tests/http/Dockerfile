FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY tests/http/go.mod ./
COPY tests/http/cmd/fault-proxy ./cmd/fault-proxy
COPY tests/http/internal/faultproxy ./internal/faultproxy
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /fault-proxy ./cmd/fault-proxy

FROM scratch
COPY --from=build /fault-proxy /fault-proxy
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/fault-proxy"]
