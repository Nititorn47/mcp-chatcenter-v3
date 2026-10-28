FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mcp-proxy .

FROM alpine:3.24

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 1001 -s /sbin/nologin app

COPY --from=builder --chown=1001:1001 /out/mcp-proxy /usr/local/bin/mcp-proxy

USER 1001

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mcp-proxy"]
