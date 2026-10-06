# 使用 Go 1.25 官方镜像作为构建环境
FROM golang:1.26 AS builder

# 禁用 CGO
ENV CGO_ENABLED=0

# 设置工作目录
WORKDIR /app

# 复制 go.mod 和 go.sum 并下载依赖
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码并构建应用
COPY . .
RUN go build -ldflags "-s -w" -o /app/duck2api ./cmd/duck2api

# 使用 Alpine Linux 作为最终镜像
FROM alpine:latest

# 设置工作目录
WORKDIR /app
# ffmpeg: internal/initialize/speech.go 的 convertAudio 调它把 WebRTC 出来的 OGG/Opus
# 转成请求的格式(mp3/wav/flac/aac)。镜像里没有这一步就只能回落成 OGG。
# 断言 libmp3lame —— mp3 是 TTS 的默认响应格式, 基础镜像换掉时让构建直接失败,
# 而不是运行时静默退化(Alpine 13x MiB 装完, 主要是这一包)。
RUN apk add --no-cache tzdata ffmpeg \
    && ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libmp3lame

# 从构建阶段复制编译好的应用和资源
COPY --from=builder /app/duck2api /app/duck2api

# 暴露端口
EXPOSE 8080

CMD ["/app/duck2api"]
