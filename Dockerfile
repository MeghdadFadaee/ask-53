FROM golang:1.26-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ask53 ./cmd/ask53
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /ask53 ./cmd/ask53

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 ask53 && adduser -D -H -u 10001 -G ask53 ask53 && mkdir -p /var/lib/ask53 && chown ask53:ask53 /var/lib/ask53
COPY --from=build /ask53 /usr/local/bin/ask53
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/ask53/
COPY third_party/ /usr/share/doc/ask53/third_party/
USER 10001:10001
WORKDIR /var/lib/ask53
EXPOSE 5353/udp 5353/tcp
ENTRYPOINT ["/usr/local/bin/ask53"]
CMD ["-config", "/etc/ask53/config.json"]
