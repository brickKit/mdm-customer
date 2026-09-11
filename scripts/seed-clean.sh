#!/usr/bin/env bash
# 撤销 seed.sh 灌的数据。反查 command_idempotency 表拿真实行 id 再精确
# 删除，不靠名字模糊匹配——防止这个环境里将来混进真实数据时被误删（同
# infra/seed-data/clean.sh 的既有判据）。permissions 这类"只增不改"的
# 表不在本脚本的管辖范围内，本组件没有这种表。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"

C_GRN=$'\033[32m'; C_OFF=$'\033[0m'
ok() { echo "${C_GRN}✓${C_OFF} $*"; }

psqlx() { docker exec -i be-postgres psql -U postgres -d brickkit_db -v ON_ERROR_STOP=1 "$@"; }

psqlx -q <<'SQL'
SET search_path TO mdm_customer;
DO $$
DECLARE
  cid BIGINT;
  k TEXT;
BEGIN
  FOR k IN SELECT unnest(ARRAY[
    'seed-customer-1','seed-customer-2','seed-customer-3','seed-customer-4','seed-customer-5'
  ])
  LOOP
    SELECT result_id::BIGINT INTO cid FROM command_idempotency WHERE idempotency_key = k;
    IF cid IS NOT NULL THEN
      DELETE FROM contacts WHERE customer_id = cid;
      DELETE FROM billing_infos WHERE customer_id = cid;
      DELETE FROM customers WHERE id = cid;
    END IF;
  END LOOP;

  -- command_idempotency 本身也清掉，不然重新 seed 时 claim-first 幂等
  -- 会直接返回"已经建过"，拿到的是已经被删掉的旧 id。
  DELETE FROM command_idempotency WHERE idempotency_key LIKE 'seed-customer-%';
END $$;
SQL

ok "mdm-customer 种子数据已清空"
