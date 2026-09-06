# mdm-customer · AI 助手导读

## 身份证

| 项 | 值 |
|---|---|
| 组件 ID | `mdm/customer` |
| 仓库名 | `mdm-customer` |
| 端口 | HTTP `8080` / gRPC `9090`（`registry/ports.tsv`，装配仓库根目录那份） |
| schema / role | `mdm_customer` / `mdm_customer_rw`（归档 schema `mdm_customer_archive` 建了但不用，见设计计划 §7） |
| 语言 / 框架 | Go：Gin + `database/sql` + `pgx/v5/stdlib` + `sqlc` + `golang-migrate` |
| 合并部署时进 | 外壳一 `go-core` |
| 装配角色 | `default` |
| 设计真相源 | 装配仓库 `docs/design/mdm-customer.md`——本文件与它冲突时，以那份为准，回来改这里 |

## 边界

**归我：** 客户名称、税号、信用额度、状态（草稿/启用/停用）、联系人、开票信息——这四类数据的唯一真相源。

**不归我：**
- 客户在销售流程中的**归属关系**（公海/私海/负责人）与**跟进状态**：归 `crm-customer`，它只存本组件主键的外键 + 摘要副本（设计书 §5.4）
- 客户与订单/发票的交易记录：归 `erp-sales` / `erp-finance`

`data_scopes: none`（设计书 §14.2.2：`mdm` 4 个组件全部 `none`）——客户主数据全员可见，不做行级过滤。

## 契约面与事件

**gRPC `mdm.customer.v1.CustomerService`：** `Create` / `Update` / `SetStatus` / `AddContact`（命令）、`Get` / `List` / `BatchGet` / `GetSummary`（读）。`BatchGet` 是 BFF 层防 N+1 的唯一合法调用方式，任何时候都不许删掉它只留 `Get`。

**REST 前缀：** `/mdm/customer/**`。`BatchGet` 不暴露到 REST——它是给其他组件 gRPC 客户端用的批量读优化，不是终端用户的操作。

**发布事件：** `mdm.customer.created.v1` / `.updated.v1` / `.disabled.v1`，全部走 Outbox，下游按 `version` 单向递增更新摘要副本。

**消费事件：** 无——消费一条就是在给只读枢纽加一条依赖边。

## 依赖与「为什么不依赖某某」

`dependencies.components` 永远是空数组。

- **不依赖任何业务组件**：`mdm` 是只读枢纽，被所有人读、自己不调任何人（设计书 §2.6 三枢纽）。这不是"暂时没有依赖"，是这个组件存在的设计前提——加一条进去，枢纽就变成了链上一环。
- **不依赖 `infra-iam-casdoor`**：IAM 走 JWT 本地验签（决策 87），只需要 `iamJwksUrl` 拉公钥，不构成依赖边。
- **不依赖事件总线（NATS）作为组件**：走 `resources` 注入（`kind: mq`），它是基础资源不是组件（设计书 §2.7.0 形态 A）。

## 这个组件特有的坑

| 不许 | 症状 | 出处 |
|---|---|---|
| 给 `dependencies.components` 加任何一条 | 编译、启动、测试全都正常——**没有任何症状**。但 `mdm` 从只读枢纽变成了链上一环，下一个人照着加第二条，同步图迟早成环 | §2.6、§4.2 |
| 省掉 `BatchGet` 只留 `Get` | 单测照样绿。但 BFF 的 GraphQL Resolver 只能退化成循环调 `Get` 的瀑布流，10 个订单查客户就是 10 次 RPC | §3.8、决策 23 |
| 把 `customers`/`contacts`/`billing_infos` 建成分区表，或往里塞归档逻辑 | 迁移能跑通、代码能编译——但这三张表是主数据不是交易流水，§11.2.5 的分区大表清单里没有 `mdm-*`；建成分区表之后回头想撤销，代价是一次真实的数据迁移 | 设计计划 §7 |
| 往 `contacts`/`billing_infos` 加 `owner_id`/`dept_path` 这类数据权限列 | 建表能过，迁移能跑——但本组件 `data_scopes: none`，加了这些列没有任何代码会去用它们过滤，纯粹是死配置，且与"客户主数据全员可见"的设计矛盾 | §14.2.2、§14.2.5 |

## 改代码前的自查

1. **我是不是在给这个组件加一条 `dependencies.components`？** 停下——先回设计计划确认这条边是不是真的必要，几乎总是不必要（见上表第一条）。
2. **我写的这段逻辑，是不是应该属于 `crm-customer`（销售归属关系）而不是这里？** 判据：这段逻辑改变的是"客户是谁"还是"谁在跟这个客户打交道"——前者归这里，后者归 `crm-customer`。
3. **我是不是在给这三张表加分区/归档/数据权限列？** 停下——设计计划 §7、§14.2.2 已经判定不需要，除非设计计划本身先改。
4. **这个改动会不会让 `contracts/customer.proto` 出现破坏性变更（删字段、改类型、改 Tag 编号）？** 下游 `erp-sales`/`crm-*` 都消费这份契约，签名一旦发布只能向后兼容地追加（§3.4 铁律 3）。
