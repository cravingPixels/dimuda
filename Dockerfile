FROM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/dimuda ./cmd/dimuda

FROM alpine:3.24.3
# git: clones GIT_REPOS; ca-certificates: TLS to registries and GitHub.
RUN apk add --no-cache git ca-certificates && mkdir /data
COPY --from=build /out/dimuda /usr/local/bin/dimuda
COPY overrides.yaml /app/overrides.yaml
# Starts as root only to create and chown /data, then switches to PUID:PGID (default 65532).
ENV DATA_DIR=/data HOME=/tmp
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/dimuda"]
