-- mdm-customer 主表。schema 由迁移工具的 search_path 指定，SQL 里不写限定名
-- ⚠️ 迁移状态表必须落在本组件 schema 里（§11.2.3）：
--    golang-migrate 的 x-migrations-table + search_path，见 backend/cmd/migrate/main.go
--    默认往 public 写会让 62 个组件的迁移记录互相顶掉
--
-- ⚠️ 不分区：这三张表是主数据（客户/联系人/开票信息），不是交易流水。
-- §11.2.5 需要分区的大表清单里没有 mdm-*，量级是"公司数量级"（几千到
-- 几万行），不会像订单那样无限增长（设计计划 §7）。不分区也让 contacts/
-- billing_infos 能对 customers 加真正的外键约束——分区表做外键要求引用
-- 列包含分区键，反而更麻烦。

CREATE TABLE customers (
    id           BIGSERIAL PRIMARY KEY,
    code         TEXT           NOT NULL,
    name         TEXT           NOT NULL,
    tax_no       TEXT           NOT NULL DEFAULT '',
    credit_limit NUMERIC(18,2)  NOT NULL DEFAULT 0,
    -- §11.2.1 强制字段
    created_at   TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ    NOT NULL DEFAULT now(),
    version      BIGINT         NOT NULL DEFAULT 1,
    status       TEXT           NOT NULL DEFAULT 'ACTIVE'
);

CREATE UNIQUE INDEX customers_code_uniq ON customers (code);
CREATE INDEX customers_status ON customers (status);

CREATE TABLE contacts (
    id          BIGSERIAL PRIMARY KEY,
    customer_id BIGINT      NOT NULL REFERENCES customers (id),
    name        TEXT        NOT NULL,
    phone       TEXT        NOT NULL DEFAULT '',
    email       TEXT        NOT NULL DEFAULT '',
    is_primary  BOOLEAN     NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    version     BIGINT      NOT NULL DEFAULT 1,
    status      TEXT        NOT NULL DEFAULT 'ACTIVE'
);
CREATE INDEX contacts_customer ON contacts (customer_id);

CREATE TABLE billing_infos (
    id           BIGSERIAL PRIMARY KEY,
    customer_id  BIGINT      NOT NULL REFERENCES customers (id),
    title        TEXT        NOT NULL,
    tax_no       TEXT        NOT NULL DEFAULT '',
    bank_name    TEXT        NOT NULL DEFAULT '',
    bank_account TEXT        NOT NULL DEFAULT '',
    address      TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    version      BIGINT      NOT NULL DEFAULT 1,
    status       TEXT        NOT NULL DEFAULT 'ACTIVE'
);
CREATE INDEX billing_infos_customer ON billing_infos (customer_id);
