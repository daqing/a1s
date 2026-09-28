# A1s

A1s 是一个简化版容器编排系统,提供 Kubernetes 最核心的高可用能力——容器监控、
故障自动重启、跨 worker 自动迁移——同时远比 Kubernetes 易于理解和运维。它不采用
Kubernetes 的声明式 YAML 模型和陡峭学习曲线,而是提供一组少量、命令式的操作。

## 动机

Kubernetes 是强大的自动化运维平台,但设计过于复杂:开发者必须先掌握大量概念
(Pod、Deployment、Service、ReplicaSet、Ingress 等)才能上手,学习曲线陡峭、
门槛高。

而底层需求始终存在:后端需要一套保证高可用的系统。A1s 只保留这一核心能力,
去掉让 Kubernetes 难以接近的其余一切。

## 范围

A1s 实现三项核心能力:

1. **容器监控**——跟踪运行中容器的健康与状态。
2. **自动重启**——重启失败的容器。
3. **自动迁移**——worker 节点宕机时,把它的容器重新调度到存活的 worker 上。

不在范围内:服务发现、网络/Ingress、配置管理、滚动升级,以及 Kubernetes
功能集的其余部分。

## 架构

一个 Go 二进制(`a1s`)、五个子命令,通过 PostgreSQL 协作:

- **`a1s api`** —— HTTP 控制面。用户命令的唯一写路径;从不直接与 worker 通信。
- **`a1s scheduler`** —— 把 pending 容器指派给 worker(最少负载优先)并排队
  start 命令。
- **`a1s monitor`** —— 把心跳超时的 worker 标记为 lost,把它的运行时容器重新
  排队到存活者上,并按重启策略重排失败容器。
- **`a1s worker`** —— 每个 worker 节点上的代理:通过 **containerd** 拉取并运行
  容器、发送心跳、执行排队的命令、回报观测到的状态。
- **`a1s run|ps|stop|rm|workers|stats`** —— 无状态 CLI 客户端,通过 HTTP 与
  API 通信。

设计不变量:

- **全部状态都在 PostgreSQL 里。** 控制面进程不在本地持久化任何东西;任意多个
  实例可以挂在普通轮询负载均衡后面。
- **处处乐观锁。** 每一次状态迁移都是版本护栏更新,竞争进程绝不可能重复生效
  (见 `docs/state-model.md`)。
- **对账**(`docs/state-model.md`):worker 每个轮询周期上报它真实管理的容器
  集合,API 修复漂移——缺失的运行时重新排队、丢失的 stop 重发、幽灵运行时清理。
- **故障检测**:worker 心跳;心跳超时的 worker 被判 lost,其容器迁移。时间线
  计算见 `docs/state-model.md`。
- **运行时**:containerd,经由官方 Go 客户端(决策依据见 `docs/architecture.md`)。

## 快速开始

要求:Go 1.27+、Docker(用于 PostgreSQL 和 containerd)、curl。

一条命令的方式——`just cluster` 会启动 postgres 与 containerd 节点、执行迁移,
并启动 api、scheduler、monitor 和两个 worker(日志与 pid 在 `.cluster/`;
`just cluster-down` 停止它们):

```sh
just cluster
```

下面是 `just cluster` 背后的完整步骤,便于理解:

```sh
# 1. PostgreSQL
docker run -d --name a1s-pg -e POSTGRES_USER=a1s -e POSTGRES_PASSWORD=a1s \
  -e POSTGRES_DB=a1s -p 127.0.0.1:5432:5432 postgres:16

# 2. containerd 节点(细节见 docs/architecture.md;代理变量仅在拉取镜像需要
#    代理的网络环境下需要)
docker run -d --name a1s-containerd --privileged -p 127.0.0.1:60001:60001 \
  -e HTTPS_PROXY=http://http.docker.internal:3128 \
  -e HTTP_PROXY=http://http.docker.internal:3128 \
  -e NO_PROXY=localhost,127.0.0.1 \
  alpine:latest \
  sh -c 'apk add --no-cache containerd socat runc >/dev/null && \
    (containerd >/var/log/containerd.log 2>&1 &) && sleep 2 && \
    socat TCP-LISTEN:60001,fork,reuseaddr UNIX-CONNECT:/run/containerd/containerd.sock'

# 3. 数据库迁移
go build -o bin/a1s .
A1S_DSN='postgres://a1s:a1s@127.0.0.1:5432/a1s?sslmode=disable' go run . db:migrate

# 4. 启动集群
A1S_DSN='postgres://a1s:a1s@127.0.0.1:5432/a1s?sslmode=disable' ./bin/a1s api &
A1S_DSN='postgres://a1s:a1s@127.0.0.1:5432/a1s?sslmode=disable' ./bin/a1s scheduler &
A1S_DSN='postgres://a1s:a1s@127.0.0.1:5432/a1s?sslmode=disable' ./bin/a1s monitor &
A1S_INTERNAL_TOKEN=dev-token A1S_API_URL=http://127.0.0.1:1905 \
A1S_CONTAINERD_ADDR=tcp://127.0.0.1:60001 A1S_CONTAINERD_SNAPSHOTTER=native \
  ./bin/a1s worker --name w1 &
```

> macOS 上 worker 必须运行在与其 containerd 共享文件系统的 Linux 主机上——上面
> 的双容器布局是最简单的本地形态,原因见 `docs/architecture.md`。

然后:

```sh
export A1S_API_URL=http://127.0.0.1:1905
./bin/a1s run nginx --name web --restart-policy on-failure
./bin/a1s ps
./bin/a1s stats
./bin/a1s stop web   # 按 id;见 ./bin/a1s ps
```

自动化端到端 chaos 验收——双 worker、`kill -9`、策略重启——一条命令(同样是
CI 所跑的):

```sh
scripts/e2e-chaos.sh          # macOS:A1S_E2E_CONTAINER_PROXY=http://http.docker.internal:3128
```

### 完全不依赖 Docker:Lima

`scripts/vm.sh` 在一个纯 Lima 虚拟机上运行同样的集群——不用 Docker,也不用
Colima。VM 原生承载 containerd、PostgreSQL 和三个 worker(overlayfs 可用、
无需折腾代理);只有控制面留在宿主机上:

```sh
just vm          # 供应(首次启动下载镜像)并启动
just vm-down     # 停止进程;VM 继续保留
```

`limactl delete a1s` 可整体重置 VM。见 `docs/architecture.md`。

## 配置

每个变量及默认值都写在 `.env.example`。速览:

| 变量 | 默认 | 使用者 | 用途 |
| --- | --- | --- | --- |
| `A1S_DSN` | — | api, scheduler, monitor, stats | 保存全部状态的 PostgreSQL DSN |
| `A1S_API_URL` | `http://127.0.0.1:1905` | worker, CLI | 控制面地址 |
| `A1S_INTERNAL_TOKEN` | — | api, worker | 内部 API 共享 bearer token;**必填** |
| `AIRWAY_PORT` | `1905` | api | 监听端口 |
| `A1S_HEARTBEAT_INTERVAL` | `5s` | worker | 心跳间隔 |
| `A1S_COMMAND_INTERVAL` | `2s` | worker | 命令轮询/状态汇报间隔 |
| `A1S_SCHEDULER_INTERVAL` | `3s` | scheduler | 指派循环间隔 |
| `A1S_HEARTBEAT_TIMEOUT` | `15s` | monitor | 心跳超时判定为 lost |
| `A1S_MONITOR_INTERVAL` | `5s` | monitor | 恢复循环间隔 |
| `A1S_CONTAINERD_ADDR` | `/run/containerd/containerd.sock` | worker | containerd socket 或 `tcp://host:port` |
| `A1S_CONTAINERD_NAMESPACE` | `a1s` | worker | containerd 命名空间 |
| `A1S_CONTAINERD_SNAPSHOTTER` | `overlayfs` | worker | 拉取与容器的快照器 |
| `A1S_CONTAINERD_PLATFORM` | 本机 | worker | 跨平台客户端的拉取/spec 平台覆盖 |

Airway 框架变量(`AIRWAY_ENV`、`AIRWAY_PORT`、`URL_PREFIX`、`STORAGE_*`)保持
框架原名;见 `.env.example`。

## 失败语义

- **控制面进程是可丢弃的。** 杀掉 api、scheduler、monitor 都不会影响运行中的
  容器。每个进程跑多实例都安全:每次写入都是版本护栏迁移,冲突通过重读解决。
- **worker 宕机**(心跳超过 `A1S_HEARTBEAT_TIMEOUT`)被标记 `lost`;其
  `scheduled`/`running` 容器回到 `pending` 由调度器重新放置。desired 状态
  (`stopped`、`failed`)不会被接管重启。`lost` worker 只能通过重启代理重新
  注册回归。
- **容器意外死亡**会在一个状态汇报周期内被 worker 上报为 `failed`;monitor 按
  `restart_policy`(`no` | `on-failure[:N]` | `always` | `unless-stopped`)重新
  排队,连续失败计数在报告 `running` 时清零。
- **漂移**(运行时被系统外手段删除、stop 命令丢失、行删除后的幽灵容器)由
  worker manifest 对账在一个轮询周期内发现并自动修复。

完整状态机与迁移执行者:`docs/state-model.md`。线上协议:`docs/api.md`。
云端部署:`deploy/tencent-cloud.md`。

## 实现基础:Airway

A1s 完全构建在 [Airway](https://github.com/daqing/airway) 之上——一个受 Ruby on
Rails 启发的 Go 全栈框架,而不是从 Go 标准库逐一拼装:

- **`lib/repo`**(基于泛型的 repository)承担全部数据库访问。
- **SQL Builder**(`lib/sql`,pg 方言)承担手写查询,如调度器的 worker 选择。
- **Schema 驱动的迁移**与脚手架 CLI 承担 schema 管理。
- **基于 Gin 的 HTTP 服务与 CLI 命令体系**——所有进程都是同一个二进制的子命令,
  共享同一份代码与模型定义。

Airway 未覆盖的组件,主要是 worker 使用的 containerd 客户端,采用官方上游 Go 库。

## 开发

```sh
go build ./...            # 构建
go test ./...             # 单元测试(未设 A1S_TEST_DSN 时跳过数据库测试)
scripts/e2e-chaos.sh      # 完整端到端 chaos 验收
```

数据库测试使用从 `A1S_TEST_DSN` 派生的每包独立数据库(见 `internal/testdb`)。
所有进程的日志都是带 `role` 字段的 JSON 行,多进程运行的日志可用标准工具过滤。

## 许可证

[MIT](LICENSE)

---

[English](README.md) | [简体中文](README.zh-CN.md)
