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
（Task 16 实现完成后补：装配路径 + 单独跑的完整命令）

## 怎么用
（Task 16 后补：一条 curl + 一条 grpcurl）

## 配置项
（Task 13 写完 component.yaml 后补，平台注入的保留变量单列一段）

## 参考实现
| 项目 | 看的模块 | 借鉴了什么 | 许可证（已复核） | 用法 |
|---|---|---|---|---|
| Odoo 17.0 | `addons/base` 的 `res.partner` | 一张 partner 表兼容客户/联系人的取舍，以及它专门处理了哪些边界情形 | LGPL-3 | 借鉴逻辑 |
| Apache OFBiz 18.12 | `applications/party` 实体模型 | Party/Contact/PostalAddress 的关系与完整度 | Apache-2.0 | 借鉴逻辑 |

**要避免它的什么**：Odoo 的 `res.partner` 把公司、个人、地址、银行账户全塞一张表——
我们按数据所有权拆开（三测试），且销售归属关系归 `crm-customer` 不归这里。

## 边界与禁令
- 客户在销售流程中的**归属关系**（公海/私海/负责人）与**跟进状态**归 `crm-customer`，
  不归这里——那个组件只存本组件主键的外键 + 摘要副本（设计书 §5.4）
- `dependencies.components` **永远是空的**：`mdm` 是只读枢纽，被所有人读、
  自己不调任何人（§2.6）。加一条进去就是把枢纽变成了链上一环
