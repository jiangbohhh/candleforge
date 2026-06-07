# ── 构建阶段 ──
FROM golang:1.23 AS builder

WORKDIR /src

# 可选 GOPROXY（默认官方；网络受限时用 --build-arg GOPROXY=... 注入）
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

# 先拷贝 go.mod/go.sum 利用缓存
COPY backend-go/go.mod backend-go/go.sum ./backend-go/
WORKDIR /src/backend-go
RUN go mod download

# 拷贝源码（pb 在 backend-go/pb 内，随 backend-go 一起）
COPY backend-go/ ./

# 编译静态二进制
RUN CGO_ENABLED=0 GOOS=linux go build -o /candleforge-server ./cmd/server

# ── 运行阶段 ──
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /candleforge-server /candleforge-server

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/candleforge-server"]
