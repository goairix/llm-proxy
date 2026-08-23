FROM golang:1.25.4 AS builder

RUN /bin/cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
 && echo 'Asia/Shanghai' >/etc/timezone

# 支持构建时覆盖 GOPROXY，默认使用官方代理
#ARG GOPROXY=https://goproxy.cn,direct
#ENV GOPROXY=${GOPROXY}

WORKDIR /build

# 优先复制依赖文件，利用 Docker 层缓存
# 依赖未变化时跳过 go mod download，加速重复构建
COPY go.mod go.sum ./
RUN go mod download

# 复制全部源码（含 internal/dashboard/web/index.html，go:embed 需要）
COPY . .

# 编译：关闭 CGO，生成静态二进制，-s -w 去除调试信息以缩减体积
RUN CGO_ENABLED=0 \
    go build -ldflags="-s -w" -trimpath \
    -o llm-proxy ./cmd/proxy

FROM alpine:3.22

# 设置时区为中国标准时间
ENV TZ=Asia/Shanghai

WORKDIR /app

# 仅拷贝编译产物，镜像中不包含 Go 工具链
COPY --from=builder /build/llm-proxy .
COPY --from=builder /usr/share/zoneinfo/Asia/Shanghai /usr/share/zoneinfo/Asia/Shanghai

# 预建日志目录（config.yaml 中 log.file 默认写入 ./logs/）
RUN mkdir -p /app/logs

# config.yaml 通过挂载注入，不内嵌到镜像（避免将密钥打包进镜像）
# 运行时挂载示例：
#   -v /path/to/config.yaml:/app/config.yaml
EXPOSE 8080

ENTRYPOINT ["/app/llm-proxy"]
