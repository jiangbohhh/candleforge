# CandleForge 🕯️🔨 — 总体计划 (PLAN.md)

> **CandleForge** · *Forge your trading strategies.*
> 一个加密货币量化交易系统：行情看板 · 策略回测 · 模拟盘 · 实盘自动交易。
> 后端 Go（主链路）+ Python（backtrader 回测）+ React/TS 前端，全容器化（docker-compose / K8s）。

> 本文件是项目蓝图。请直接在此修改：勾选/取消功能 `[ ]` → `[x]`，删除不需要的模块，补充你的需求。
> 我会以本文件为准来逐步实现。最后更新规则：每完成一个里程碑，在对应项打勾。

---

## 0. 项目定位（已确认）

| 维度 | 选择 |
|------|------|
| 核心能力 | 行情+组合跟踪、策略回测、模拟盘交易、实盘自动交易 |
| 技术栈 | **Go 后端(主服务) + Python 回测服务(瘦) + React/TS 前端** |
| 架构形态 | **混合架构(Python 瘦身)**：Go 管几乎一切(数据采集/交易/Web/账户/订单/风控/实时)；**Python 只管 backtrader 回测**；gRPC 通信，共享 PG |
| 市场 | **🎯 第一阶段：仅加密货币（Binance）**；A 股 → 未来功能（见 §14） |
| 数据源 | Binance 公开 API（行情）；数据层做接口抽象，未来可扩展 |
| **部署形态** | **全容器化**：本地开发 docker-compose；部署/扩展 K8s(Helm chart)。详见 §15 |
| **加密交易所** | Binance 现货（先现货，合约/杠杆未来再说） |
| **首个交付** | **M1 行情看板最小闭环** |

**实现顺序原则**：行情/数据 → 回测 → 模拟盘 → 实盘。每一层复用前一层，降低风险。

---

## 0.1 本地环境现状（已探测 2026-06-03）

| 工具 | 状态 |
|------|------|
| **Go** | ✅ 1.23.3 (darwin/arm64) — 主服务 |
| **protoc** | ✅ 25.3 — gRPC 契约编译（还需装 protoc-gen-go / protoc-gen-go-grpc / grpcio-tools） |
| Python | ✅ 3.12.2 — 量化服务 |
| Node.js / npm | ✅ v24.5.0 / 11.5.1 — 前端 |
| Docker | ✅ 28.0.1 |
| PostgreSQL | ❌ 未安装 → **计划用 Docker 跑 PG 容器**（已确认 M1 直接用 PostgreSQL） |

---

## 1. 技术栈（混合架构 · Python 瘦身版）

> **为什么仍混合**：只做加密后，数据(go-binance)和交易(Binance API)Go 都能干，唯一锁 Python 的是
> 成熟回测引擎 **backtrader**。故让 Go 承担几乎一切，Python 瘦成一个**只跑回测**的服务。
> 好处：主链路单语言(Go)简单高效；回测复用 backtrader 不重造轮子。

### 后端主体 — Go 主服务（几乎一切）
- **Web 框架**：Gin 或 Echo（REST + 中间件）
- **WebSocket**：gorilla/websocket，推送行情与订单状态
- **Binance 对接**：`go-binance`（adshao/go-binance）—— REST 取 K 线 + WebSocket 订实时 + 现货下单
- **gRPC 客户端**：仅在回测时调用 Python（google.golang.org/grpc）
- **ORM**：GORM 或 sqlc
- **调度**：robfig/cron
- **职责**：**数据采集、实时行情、交易下单**、Web API、WebSocket、账户/订单/持仓、策略配置、风控、调度

### 后端辅助 — Python 回测服务（瘦，仅 M2 用）
- **服务框架**：gRPC server（grpcio）
- **回测引擎**：`backtrader` + pandas/numpy
- **职责**：**只做一件事**——接收 Go 发来的(历史K线 + 策略参数)，跑 backtrader，返回绩效与净值曲线
- **不碰**：不采数、不交易、不连前端。数据由 Go 从 PG 读出后通过 gRPC 传入。

### 通信与数据
- **Go ↔ Python**：gRPC，proto 定义契约。核心方法基本只有 `RunBacktest`。
- **共享数据库**：PostgreSQL。Go 读写全部业务表；Python 回测服务可无状态(数据靠 gRPC 传入)或只读 PG。

### 前端
- **框架**：React + TypeScript + Vite（默认）/ 或 Vue3
- **UI 库**：Ant Design（金融后台常用）
- **图表**：TradingView Lightweight Charts（K 线）+ ECharts（组合/绩效）
- **状态管理**：Zustand / Redux Toolkit
- **实时**：WebSocket 客户端订阅行情与订单

### 数据库
- **关系库**：PostgreSQL（账户/订单/持仓/策略配置/K线）。K 线量大时再加 TimescaleDB 插件。
- **缓存**：Redis（实时行情缓存、WebSocket 广播、分布式锁）。K8s 多副本场景下，分布式锁靠 Redis（见 §15 单例约束）。

### 部署
- 全容器化，docker-compose（本地）+ Helm/K8s（部署）双轨。**详见 §15 部署与容器化**。

---

## 2. 系统架构（Go 主导 + Python 瘦回测）

```
┌──────────────┐   REST / WebSocket   ┌─────────────────────────────┐      ┌──────────────┐
│  前端 React  │ <─────────────────>  │      Go 后端（主服务）       │ ───> │   Binance    │
│   + TS       │                      │  ┌───────────────────────┐  │ REST │   API        │
└──────────────┘                      │  │ Binance 数据采集/交易  │  │  +WS │ (行情+下单)  │
                                      │  │ Web API / WebSocket   │  │ <─── └──────────────┘
                                      │  │ 账户 / 订单 / 持仓     │  │
                                      │  │ 策略配置 / 风控 / 调度 │  │
                                      │  └──────────┬────────────┘  │
                                      └─────────────┼───────────────┘
                                          gRPC: RunBacktest（仅回测时）
                                      ┌─────────────┼───────────────┐
                                      │  Python 回测服务（瘦/无状态）│
                                      │  ┌───────────────────────┐  │
                                      │  │ backtrader 回测引擎    │  │
                                      │  │ 入: K线+策略参数        │  │
                                      │  │ 出: 绩效+净值曲线       │  │
                                      │  └───────────────────────┘  │
                                      └─────────────────────────────┘
                              ┌────────────────────────────────────┐
                              │   PostgreSQL（Go 读写全部业务表）   │
                              └────────────────────────────────────┘
```

**职责边界**：Go = 主链路全包（数据/交易/Web/账户/风控）；Python = 只接 `RunBacktest`，跑 backtrader 返回结果。
**契约**：`proto/quant.proto` 核心方法 `RunBacktest`（入参 K线+策略+参数，返回绩效指标+净值+交易点）。

---

## 3. 数据层（加密货币 / Binance）

**目标**：用接口抽象 `MarketDataSource`，当前只有 Binance 实现，未来可加 A 股(见 §14)。**全部在 Go 侧实现**。

### 3.1 Binance 数据接口
- **REST**：`/api/v3/klines` 取历史 K 线（支持 1m/5m/15m/1h/4h/1d 等周期）
- **WebSocket**：`@kline_<interval>` 订阅实时 K 线、`@ticker` 订实时价、`@depth` 订盘口
- **库**：Go 用 `adshao/go-binance/v2`（覆盖现货行情 + 交易）
- **限频**：Binance 有 weight 限制，需做请求节流与重试

### 3.2 数据特点（相比 A 股的简化）
- ✅ 7×24 交易，无开收盘/停牌；无复权概念；标的可无限拆分
- ✅ 公开行情免费、稳定、文档好；实盘 API 正规，可全自动
- 周期统一用 UTC 时间戳，避免时区坑

### 3.3 实现清单（Go 侧）
- [ ] 定义接口 `MarketDataSource`（`GetSymbols`, `GetKline`, `SubscribeTicker`, `SubscribeKline`）
- [ ] `BinanceSource`：REST 取历史 + WebSocket 订实时
- [ ] 历史 K 线落库 PostgreSQL（多周期表/分区）
- [ ] 增量更新调度：WebSocket 持续接收，断线重连；定时补全缺口
- [ ] 数据校验与去重
- [ ] 实时行情进程内缓存（后续可上 Redis），经 WebSocket 广播给前端

**符号体系**：统一 symbol 格式，如 `CRYPTO.BTC-USDT`（为未来多市场预留前缀，如 `CN.600519`）。

---

## 4. 数据库设计（核心表）

- [ ] `users` 用户表
- [ ] `symbols` 标的元数据（代码、名称、市场、类型）
- [ ] `kline_*` K 线时序表（按周期分表/分区）
- [ ] `accounts` 账户（模拟/实盘，资金、可用余额）
- [ ] `positions` 持仓（标的、数量、均价、浮动盈亏）
- [ ] `orders` 订单（委托、状态、成交明细）
- [ ] `trades` 成交流水
- [ ] `strategies` 策略配置（参数、状态、绑定账户）
- [ ] `backtest_runs` 回测记录（参数、绩效指标、净值曲线）
- [ ] `watchlist` 自选列表

---

## 5. 回测引擎（里程碑 B）

- [ ] 选型决策：`backtrader`（成熟）/ `vnpy`（偏实盘）/ 自研（可控）
- [ ] 策略基类：`on_bar` / `on_tick` 事件驱动接口
- [ ] 撮合假设：手续费、滑点、A股 T+1 与涨跌停、加密 24h 交易
- [ ] 绩效指标：年化收益、最大回撤、夏普、胜率、盈亏比
- [ ] 净值曲线 / 交易点位输出给前端可视化
- [ ] 参数优化（网格/遍历，可选）
- [ ] 示例策略：双均线、RSI、网格（用于打通全链路）

---

## 6. 模拟盘 / 撮合引擎（里程碑 C）

- [x] 模拟撮合：基于实时行情按价格撮合委托
- [x] 账户体系：模拟资金账户，支持下单/撤单/查询
- [x] 订单状态机：待报→已报→部成→全成/已撤/废单
- [x] 复用风控引擎（见 §7）
- [ ] 策略可一键从回测切到模拟盘（同一策略代码）

---

## 7. 风控引擎（实盘前必备，Go 实现）

- [x] 下单前校验：资金充足、持仓上限、单笔/单日限额（单笔名义额上限已落地）
- [ ] 加密规则：最小下单量(minQty)、价格/数量精度(tickSize/stepSize)、最小名义额(minNotional)
- [ ] 风险指标：单币种集中度、总仓位、回撤熔断
- [x] 紧急停止开关（一键平仓/停所有策略）
- [ ] （未来）A 股规则：T+1、涨跌停、最小 100 股、停牌 → 见 §14

---

## 8. 实盘交易网关（里程碑 M4，Go 实现）

### 8.1 可插拔 Broker 架构

交易通道做成**可插拔**，策略代码不变，只换 Broker 实现：

```
统一 Broker 接口（下单/撤单/查持仓/查订单/回报）
├── SimBroker          模拟盘撮合（M3，必做）
├── BinanceBroker      加密 Binance 现货 API 全自动 ✅（M4 主目标，正规无障碍）
└── (未来) QmtBroker / NotifyBroker  → A 股通道，见 §14
```

### 8.2 Binance 实盘要点
- 现货下单：限价/市价单，`go-binance` 支持
- 订单状态机：NEW → PARTIALLY_FILLED → FILLED / CANCELED / REJECTED
- 用户数据流(User Data Stream)：WebSocket 实时接收订单/余额更新
- 时间同步：Binance 对 timestamp 敏感，需校准服务器时间

### 8.3 实现清单
- [ ] 定义统一 `Broker` 接口（与 SimBroker 同接口）
- [ ] `BinanceBroker`：现货下单/撤单/查询，User Data Stream 同步
- [ ] 订单回报与持仓同步
- [ ] API 密钥安全：加密存储、最小权限(只开现货交易，不开提币)、IP 白名单

> ⚠️ 实盘涉及真实资金，模拟盘充分验证后再开启，独立审批开关。
> 💡 是否支持合约/杠杆：暂按现货设计，后续按需扩展。

---

## 9. 前端 UI 模块

- [ ] 登录 / 账户管理
- [ ] 行情看板：K 线图、盘口、自选列表、实时涨跌
- [ ] 策略管理：创建/编辑/参数配置/启停
- [ ] 回测页面：配置回测、净值曲线、绩效报告、交易明细
- [ ] 持仓与订单：实时持仓、委托/成交、手动下单
- [ ] 组合跟踪：总资产、盈亏曲线、资产配置饼图
- [ ] 风控/告警面板
- [ ] 系统设置：数据源、API key、通知配置

---

## 10. 横切关注点

- [ ] 认证授权：JWT，角色权限
- [ ] 配置管理：pydantic-settings + .env（敏感信息不入库明文）
- [ ] 日志与监控：结构化日志、关键操作审计
- [ ] 告警通知：邮件 / Server酱 / Telegram（下单、风控、异常）
- [ ] 测试：pytest 单测 + 回测黄金数据回归测试
- [ ] 错误处理与重连：数据源/交易所断线重连

---

## 11. 里程碑与交付顺序

| 里程碑 | 内容 | 产出 |
|--------|------|------|
| **M0 脚手架** | 三端骨架、proto/gRPC 打通、**各服务 Dockerfile + docker-compose 一键起**、DB 连接 | compose 跑起的可通信空壳 |
| **M1 数据+行情** | Go 接 Binance(REST+WS)采数→落库；Go 暴露 REST/WS；前端看板、自选 | 能看 BTC/ETH 等实时/历史行情 |
| **M2 回测** | Go 取历史 K 线 → gRPC 传 Python backtrader 回测、示例策略、绩效报告页 | 能跑策略回测 |
| **M3 模拟盘** | Go 撮合引擎、风控、模拟下单、持仓跟踪 | 能用模拟资金交易 ✅ |
| **M4 实盘** | Go 接 Binance 现货全自动、密钥安全、告警 | 能实盘自动交易加密 |
| **未来** | A 股接入(数据/回测/QMT/半自动)、合约杠杆等 | 见 §14 |

> **跨语言职责**：Go 负责主链路全部(数据/交易/Web/账户/风控)，Python 仅在 M2 被 gRPC 调用跑回测。M0 先打通 gRPC 骨架。

---

## 12. 项目目录结构（混合架构）

```
candleforge/
├── proto/                       # gRPC 契约（Go/Python 共享，单一事实源）
│   └── quant.proto              # RunBacktest（当前核心方法）
│
├── backend-go/                  # Go 主服务（主链路全包）
│   ├── Dockerfile               # 多阶段构建 → distroless 单二进制
│   ├── cmd/server/main.go       # 入口
│   ├── internal/
│   │   ├── api/                 # Gin 路由 (REST)
│   │   ├── ws/                  # WebSocket 行情/订单推送
│   │   ├── market/             # Binance 数据采集 (go-binance)
│   │   ├── broker/             # 交易网关 (Sim/Binance Broker)
│   │   ├── account/            # 账户/订单/持仓
│   │   ├── strategy/           # 策略配置 CRUD + 信号
│   │   ├── risk/               # 风控引擎
│   │   ├── backtestclient/     # gRPC 客户端 → Python 回测
│   │   ├── store/              # DB 访问 (GORM/sqlc)
│   │   └── config/
│   ├── pb/                      # protoc 生成的 Go 代码
│   └── go.mod
│
├── quant-py/                    # Python 回测服务（瘦，仅 backtrader）
│   ├── Dockerfile               # python slim + backtrader
│   ├── server.py                # gRPC server 入口
│   ├── backtest/                # backtrader 封装
│   ├── strategies/              # 回测用策略库
│   ├── pb/                      # protoc 生成的 Python 代码
│   └── pyproject.toml
│
├── frontend/                    # React + TS
│   ├── Dockerfile               # 构建产物 → nginx 托管
│   ├── src/{pages,components,api,store}/
│   └── package.json
│
├── deploy/                      # 部署编排（§15）
│   ├── helm/                    # Helm chart（K8s）
│   │   ├── Chart.yaml
│   │   ├── values.yaml          # 镜像tag/副本/资源/密钥引用
│   │   └── templates/           # Deployment/StatefulSet/Service/Ingress/ConfigMap/Secret
│   └── k8s-local/               # kind/minikube 本地集群配置
│
├── docker-compose.yml           # 本地开发：pg + redis + go + python + frontend
└── PLAN.md
```

> **gRPC 工作流**：改 `proto/quant.proto` → `protoc` 同时生成 Go(`backend-go/pb`) 和 Python(`quant-py/pb`) 代码 → 两端按生成的接口编码。契约即文档。

---

## 13. 已确认决策

- 部署形态：**本地单机自用**
- 第一阶段市场：**仅加密货币（Binance 现货）**，A 股推迟（见 §14）
- 后端架构：**Go 主服务（主链路全包）+ Python 瘦回测服务（仅 backtrader）**，gRPC 通信
- 前端：**React + TypeScript** + Ant Design + TradingView 图表
- 回测引擎：**backtrader**（Python）
- 数据库：**PostgreSQL**（Docker 跑）
- 加密交易所：**Binance**；合约/杠杆暂不做，先现货
- 首个交付：**M1 行情看板最小闭环**

### ❓ 仍待确认
- 历史 K 线拉取范围（几个币种、多长时间、哪些周期）
- 心里是否已有想先跑的策略

> 当前阶段：**讨论中**（暂不写代码）。确认后从 **M0 脚手架** 开始实现。

---

## 14. 未来功能（暂不实现，架构预留）

> 这些已讨论过、确定推迟。架构上用接口抽象预留扩展点，不在第一阶段实现。

### 14.1 A 股接入
- **数据**：akshare（免费实时+历史）/ baostock（历史全）/ tushare（需 token）
  - 实现为新的 `MarketDataSource` 适配器，与 Binance 同接口
- **回测**：复用 backtrader，补 A 股撮合规则
- **实盘通道**（可插拔 `Broker`）：
  - 半自动通知 `NotifyBroker`：出信号+推送，人工下单（零合规风险，门槛低）
  - QMT/迅投 `QmtBroker`：全自动，需券商权限（常 50 万资产门槛，以后再定）
  - easytrader：免费折中，有失效/合规风险
  - ⚠️ QMT 是 Windows 客户端，届时需 Windows 机跑 QMT + 交易代理服务
- **A 股专属规则**：T+1、涨跌停、最小 100 股、停牌、前/后复权
- **符号体系**已预留 `CN.600519` 前缀

### 14.2 其他候选
- 加密合约 / 杠杆交易（风控需加强）
- 多交易所（OKX 等）
- TimescaleDB（数据量大时）
- 多用户 / 云部署

---

## 15. 部署与容器化（docker-compose + K8s 双轨）

> 已确认：**全容器化**。本地开发用 docker-compose，部署/扩展用 K8s(Helm chart)，二者共用同一批镜像。

### 15.1 镜像（每个服务一个 Dockerfile）
- [ ] `backend-go`：多阶段构建（builder 编译 → 极小 distroless/alpine 运行镜像，单二进制）
- [ ] `quant-py`：python slim 基础镜像 + backtrader 依赖
- [ ] `frontend`：构建产物用 nginx 托管（生产）或 vite preview
- [ ] 镜像打 tag，推送到镜像仓库（本地/私有 registry/Docker Hub）

### 15.2 本地：docker-compose
- [ ] `docker-compose.yml`：postgres + redis + backend-go + quant-py + frontend
- [ ] 开发友好：源码挂载 + 热重载（Go 用 air，前端用 vite dev）
- [ ] `.env` 注入配置；一键 `docker compose up`

### 15.3 部署：K8s + Helm
- [ ] **Helm chart**（`deploy/helm/`），values 参数化（镜像 tag、副本数、资源、密钥引用）
- [ ] 工作负载：
  - `backend-go` Deployment + Service（注意单例约束，见下）
  - `quant-py` Deployment + Service（**可多副本 + HPA**，回测并行）
  - `frontend` Deployment + Service + Ingress
  - `postgres` **StatefulSet + PVC**（已确认）
  - `redis` Deployment（或 StatefulSet）
- [ ] **ConfigMap**（普通配置）+ **Secret**（Binance API key、DB 密码）
- [ ] Ingress 暴露前端/API；Service DNS 做 Go→Python gRPC 服务发现
- [ ] 健康检查：liveness/readiness probe
- [ ] 资源 requests/limits

### 15.4 ⚠️ 交易系统的 K8s 单例约束（关键！）
**有状态/有副作用的服务不能盲目水平扩展**：

| 服务 | 副本策略 | 原因 |
|------|----------|------|
| 行情采集(Binance WS) | **单例** replicas:1 或 leader 选举 | 多副本会重复订阅、重复写库 |
| 交易/下单 | **单例** 或分布式锁 | 多副本会重复下单、重复成交（危险） |
| **回测服务** | **可多副本 + HPA** ✅ | 无状态，K8s 在本项目的最大价值点 |
| Web/API(只读为主) | 可多副本 | 注意 WebSocket 会话粘性 |

> 实现上：把"行情采集 + 交易执行"的有状态部分与"无状态 Web 处理"在 Go 服务内分离，或拆成独立部署，便于差异化副本策略。用 Redis 锁保证下单单例。

### 15.5 环境就绪检查（动手时补）
- [ ] 本地 K8s：kind / minikube / k3d（择一，开发测试用）
- [ ] Helm CLI
- [ ] kubectl