# OmniFusion 网关镜像：两阶段构建——静态二进制（modernc.org/sqlite 纯 Go，CGO_ENABLED=0）
# + alpine 运行时（ca-certificates 供上游 HTTPS）。
#
# 构建：docker build -f deploy/Dockerfile -t omnifusion:latest .
# 国内网络：docker build --build-arg GOPROXY=https://goproxy.cn,direct -f deploy/Dockerfile -t omnifusion:latest .
# 版本注入：--build-arg VERSION=v0.1.0
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev

FROM golang:1.26-alpine AS build
ARG GOPROXY
ARG VERSION
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY sdk sdk
RUN go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/ofd ./cmd/ofd

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -g 10001 ofd \
 && adduser -D -u 10001 -G ofd ofd \
 && mkdir -p /data && chown -R ofd:ofd /data
USER ofd
WORKDIR /data
COPY --from=build /out/ofd /usr/local/bin/ofd
EXPOSE 20130
VOLUME /data
# compose 默认挂载 /etc/omnifusion/config.yaml（见 deploy/docker-compose.yml）；
# 裸 docker run 不挂配置时去掉 CMD 覆盖即可用内置默认值（仅回环监听）。
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:20130/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/ofd"]
CMD ["-config", "/etc/omnifusion/config.yaml"]
