ARG GO_IMAGE=golang:1.26.4-alpine
ARG RUNTIME_IMAGE=alpine:3.22
FROM ${GO_IMAGE} AS build
ARG VERSION=0.1.1
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/codar-gateway/codar-gateway/internal/gateway.Version=${VERSION}" -o /out/codar ./cmd/codar

FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates tzdata && addgroup -S codar && adduser -S -G codar codar && mkdir /data && chown codar:codar /data
COPY --from=build /out/codar /usr/local/bin/codar
COPY LICENSE /usr/share/licenses/codar2api/LICENSE
ENV CODAR_ADDR=0.0.0.0:8801 CODAR_DATA_DIR=/data TZ=Asia/Shanghai
WORKDIR /data
USER codar
VOLUME ["/data"]
EXPOSE 8801
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8801/ping || exit 1
ENTRYPOINT ["/usr/local/bin/codar"]
