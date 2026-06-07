<div align="center">

# CandleForge 🕯️🔨

**Forge your trading strategies.**

一个加密货币量化交易系统 — 行情看板 · 策略回测 · 模拟盘 · 实盘自动交易

[![Go](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Python](https://img.shields.io/badge/Python-3.12-3776AB?logo=python&logoColor=white)](https://www.python.org/)
[![React](https://img.shields.io/badge/React-TS-61DAFB?logo=react&logoColor=white)](https://react.dev/)
[![Docker](https://img.shields.io/badge/Docker-compose%20%2F%20K8s-2496ED?logo=docker&logoColor=white)](https://www.docker.com/)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

</div>

---

## ✨ 简介

**CandleForge** 是一个面向加密货币市场的量化交易系统，提供从**行情采集**、**策略回测**、**模拟盘**到**实盘自动交易**的完整闭环。

采用 **Go + Python 混合架构**：Go 承担主链路（数据采集、交易执行、Web API、账户、风控、实时推送），Python 作为瘦回测服务复用成熟的 `backtrader` 引擎。前端用 React + TypeScript。全程容器化，支持 docker-compose 本地开发与 K8s（Helm）部署。

> 🎯 第一阶段聚焦 **Binance 现货**。架构以接口抽象预留扩展点，A 股等市场作为未来功能。

## 🧩 核心能力

- 📈 **行情看板** — 实时/历史 K 线、自选列表、WebSocket 实时刷新
- 🔬 **策略回测** — 基于 backtrader，输出绩效指标（年化/回撤/夏普/胜率）与净值曲线
- 🧪 **模拟盘** — 基于实时行情的模拟撮合，零资金验证策略
- 🤖 **实盘交易** — 对接 Binance 现货 API，全自动下单（含风控与紧急停止）
- 🛡️ **风控引擎** — 下单前校验、仓位/集中度控制、回撤熔断

## 🏗️ 架构

```
┌──────────────┐   REST / WebSocket   ┌─────────────────────────────┐      ┌──────────────┐
│  前端 React  │ <─────────────────>  │      Go 后端（主服务）       │ ───> │   Binance    │
│   + TS       │                      │  数据采集 / 交易 / Web /     │ REST │   API        │
└──────────────┘                      │  账户 / 订单 / 风控 / 实时    │  +WS │ (行情+下单)  │
                                      └─────────────┬───────────────┘ <─── └──────────────┘
                                       gRPC: RunBacktest（仅回测时）
                                      ┌─────────────┴───────────────┐
                                      │  Python 回测服务（backtrader）│
                                      └─────────────────────────────┘
                              ┌────────────────────────────────────┐
                              │   PostgreSQL（业务数据）+ Redis     │
                              └────────────────────────────────────┘
```

**职责边界**：Go = 主链路全包（数据/交易/Web/账户/风控）；Python = 只接 `RunBacktest`，跑 backtrader 返回结果。

## 🛠️ 技术栈

| 层 | 技术 |
|------|------|
| 后端主服务 | Go 1.23 · Gin · gorilla/websocket · [go-binance](https://github.com/adshao/go-binance) · GORM/sqlc · gRPC |
| 回测服务 | Python 3.12 · backtrader · pandas/numpy · grpcio |
| 前端 | React · TypeScript · Vite · Ant Design · TradingView Lightweight Charts |
| 数据库 | PostgreSQL · Redis |
| 部署 | Docker · docker-compose · Kubernetes · Helm |

## 📦 项目结构

```
candleforge/
├── proto/          # gRPC 契约（Go/Python 共享）
├── backend-go/     # Go 主服务（数据采集/交易/Web/账户/风控）
├── quant-py/       # Python 回测服务（backtrader）
├── frontend/       # React + TypeScript
├── deploy/         # Helm chart + K8s 配置
├── docker-compose.yml
└── PLAN.md         # 总体计划与设计文档
```

## 🗺️ 路线图

| 里程碑 | 内容 | 状态 |
|--------|------|:----:|
| **M0** 脚手架 | 三端骨架、gRPC 打通、Docker/compose 一键起 | ⬜ |
| **M1** 数据+行情 | Binance 采数、落库、行情看板、自选 | ⬜ |
| **M2** 回测 | backtrader 回测、示例策略、绩效报告 | ⬜ |
| **M3** 模拟盘 | 撮合引擎、风控、模拟下单、持仓跟踪 | ⬜ |
| **M4** 实盘 | Binance 现货全自动、密钥安全、告警 | ⬜ |
| **未来** | A 股接入、合约/杠杆、多交易所 | 💡 |

> 完整设计与决策记录见 [PLAN.md](PLAN.md)。

## 🚀 快速开始

> 🚧 当前为 **M0 脚手架**：三端骨架 + gRPC 打通 + 容器化。功能将随里程碑推进。

### 方式一：Docker Compose（推荐）

```bash
cp .env.example .env
docker compose up --build

# 前端自检页 → http://localhost:5173
# 后端 API   → http://localhost:8080/healthz
```

打开前端页面点击「运行连通检查」，可验证 **Go ↔ PostgreSQL** 与 **Go ↔ Python gRPC** 三端连通。

### 方式二：本地分别启动（开发调试）

```bash
# 1) 数据库
docker compose up -d postgres

# 2) Python 回测服务
cd quant-py && pip install grpcio grpcio-tools && python server.py

# 3) Go 主服务
cd backend-go && go run ./cmd/server

# 4) 前端
cd frontend && npm install && npm run dev
```

### 重新生成 gRPC 代码

修改 `proto/quant.proto` 后：

```bash
bash proto/gen.sh   # 同时生成 Go 与 Python 的 pb 代码
```

### K8s 部署（规划中）

```bash
helm install candleforge ./deploy/helm
```

## ⚠️ 风险提示

本项目仅供学习与研究使用。加密货币交易涉及高风险，实盘自动交易可能导致**真实资金损失**。请在模拟盘充分验证后再启用实盘，并自行承担一切风险。本项目不构成任何投资建议。

## 📄 License

[MIT](LICENSE)
