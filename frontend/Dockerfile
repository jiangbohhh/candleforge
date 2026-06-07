# ── 构建阶段 ──
FROM node:22-slim AS builder

WORKDIR /app
# 可选 npm 镜像源（默认官方；网络受限时用 --build-arg NPM_REGISTRY=... 注入）
ARG NPM_REGISTRY=https://registry.npmjs.org/
COPY frontend/package*.json ./
RUN npm ci --registry=${NPM_REGISTRY}
COPY frontend/ ./
RUN npm run build

# ── 运行阶段 ──
FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
