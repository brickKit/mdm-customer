# mdm-customer · 客户主数据

客户的基础主数据：名称、税号、信用额度、状态、联系人、开票信息。

## 它能做什么
- 客户建档、改档、启用/停用
- 联系人与开票信息管理
- 供其他组件批量取客户摘要（`batchGet`，防 N+1）

## 需要哪些基础资源
| 资源 | 形态 | 为什么需要 | 怎么起 |
|---|---|---|---|
| PostgreSQL 16 | **A**（brickKit 基础资源，`kind: database`） | 数据持久化，独占 schema `mdm_customer` | 装配仓库根目录 `make up` |
| NATS 2.10 | **A**（`kind: mq`） | 发布 `mdm.customer.*` 事件（Outbox 推送） | 同上 |

⚠️ 形态 A / B / C 的区别见设计书 §2.7.0。本组件**不需要** Traefik 与 Casdoor
就能单独跑起来——它不对 IAM 建依赖边，JWT 走本地验签（决策 87）。

## 怎么起来

**装配路径**（正常场景，跟着装配仓库走）：`brickkit.yaml` 的 `components` 里有一条 `{id: mdm/customer, version: 1.0.0}`，`resources` 配好 `postgres-shared`/`nats-shared` 两个绑定即可：

```bash
cd <装配仓库根目录>
make up            # 起基础资源（PostgreSQL + NATS，见装配仓库 Makefile）
brickkit up         # 迁移容器先跑一次性迁移，成功后拉起本组件
```

**单独跑**（不经过 brickkit，本地调试用——组件本身就是独立进程，§1.5 原则一）：

```bash
cd components/mdm/customer
export COMPONENT_ID=mdm/customer COMPONENT_VERSION=1.0.0
export DATABASE_HOST=localhost DATABASE_PORT=5432 DATABASE_USER=postgres \
       DATABASE_PASSWORD=<你的密码> DATABASE_NAME=brickkit_db
export MQ_HOST=localhost MQ_PORT=4222
export PG_SCHEMA=mdm_customer OTEL_BASE_URL=

go run ./backend/cmd/migrate up   # 建表（一次性，幂等，可重复跑）
go run ./backend/cmd/server        # 监听 :8080（HTTP）与 :9090（gRPC）
```

⚠️ 迁移用 `DATABASE_USER=postgres`（超级用户），不是 `mdm_customer_rw`——建表/建分区需要表所有权，运行时的 `mdm_customer_rw` 故意只有 DML 权限（见 `docs/dev/实测踩坑记录.md` A4f）。

## 怎么用

```bash
# HTTP：创建一个客户
curl -fsS -X POST http://localhost:8080/mdm/customer/customers \
  -H 'Content-Type: application/json' \
  -d '{"idempotency_key":"demo-001","name":"示例客户","credit_limit":"50000.00"}'

# gRPC：BatchGet（额外端口没有宿主机映射，用容器的 Docker 网络 IP——
# 见 docs/dev/实测踩坑记录.md D7）
IP=$(docker inspect brickkit-be-assembly-standard-mdm-customer-1-0-0-1 \
       --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
grpcurl -plaintext -import-path contracts/mdm/customer/v1 \
  -import-path "$(go env GOPATH)/pkg/mod/google.golang.org/protobuf@v1.36.12" \
  -proto customer.proto -d '{"ids":["1","999"]}' \
  "$IP:9090" mdm.customer.v1.CustomerService/BatchGet
```

## 配置项

`component.yaml` 的 `configSchema`——只列真的被代码读取的项（曾经多声明过 `defaultPageSize`/`listWindowDays` 两项，Task 18 校准文档时发现代码从没读过它们，`List` 的默认页大小/时间窗口其实是 `be-sdk-go` 的包级常量，已经删掉，不留"看起来能配实际不能配"的死配置）：

| 键 | 默认值 | 干什么 |
|---|---|---|
| `pgSchema` | `mdm_customer` | 本组件的 PostgreSQL schema（`SET LOCAL search_path` 用它） |
| `iamJwksUrl` | 无（弱依赖缺失时变量根本不存在，§3.6） | JWT 本地验签拉公钥的地址——不建依赖边（决策 87） |
| `otelBaseUrl` | 空 | 留空即 Blackhole Exporter（不联网、不阻塞），有值才真的导出 trace |

平台保留变量（`DATABASE_*`/`MQ_*`/`COMPONENT_ID`/`COMPONENT_VERSION`）由 brickKit 注入，组件代码不读、也不许在 `configSchema` 里起同名项（§2.7.3）。

## 参考实现
| 项目 | 看的模块 | 借鉴了什么 | 许可证（已复核） | 用法 |
|---|---|---|---|---|
| Odoo 17.0 | `addons/base` 的 `res.partner` | 一张 partner 表兼容客户/联系人的取舍，以及它专门处理了哪些边界情形 | LGPL-3 | 借鉴逻辑 |
| Apache OFBiz 18.12 | `applications/party` 实体模型 | Party/Contact/PostalAddress 的关系与完整度 | Apache-2.0 | 借鉴逻辑 |

**要避免它的什么**：Odoo 的 `res.partner` 把公司、个人、地址、银行账户全塞一张表——
我们按数据所有权拆开（`customers`/`contacts`/`billing_infos` 三张表），且销售归属关系归 `crm-customer` 不归这里。

## 边界与禁令
- 客户在销售流程中的**归属关系**（公海/私海/负责人）与**跟进状态**归 `crm-customer`，
  不归这里——那个组件只存本组件主键的外键 + 摘要副本（设计书 §5.4）
- `dependencies.components` **永远是空的**：`mdm` 是只读枢纽，被所有人读、
  自己不调任何人（§2.6）。加一条进去就是把枢纽变成了链上一环
