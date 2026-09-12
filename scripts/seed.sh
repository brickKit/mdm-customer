#!/usr/bin/env bash
# 本组件自己的种子数据：12 个示例客户，覆盖 ACTIVE/DISABLED 两种状态 +
# 多个行业画像 + 信用额度从 0 到百万级的完整区间（总纲 SOP-W-7"数据
# 种类越多越好"——量大到能体现真实感，也让下游 crm-opportunity/erp-sales
# 有得挑：比如需要"一个信用额度极低、真会触发 Saga 补偿"的客户做演示，
# 这里就直接有）。
#
# ⚠️ seed-customer-1..5 是已经被 crm-opportunity 反查引用过的固定
# idempotency_key（总纲 SOP-W-7"种子数据是轻量契约"）——只增不改，新增
# 客户一律往后接 seed-customer-6 起，不改前 5 个的任何字段。
#
# ⚠️ 只给本地开发/演示用，不出现在任何部署/CI 流程里。数据都是假的，
# 灌进的是真实运行中的本组件数据库（用户已明确同意，见根仓库
# feedback_direct_db_seeding_ok 记录）。全程走真实 gRPC 调用，不是直接
# 写库——claim-first 幂等（固定 idempotency_key），重复跑不会重复建。
# 唯一的例外是收尾那一小段"时间跨度"回填（见文件末尾），直接 UPDATE
# created_at——customers 表不分区，不存在分区放错的风险，纯粹是为了让
# "最近 N 天"这类查询、列表默认排序有真实的新旧之分可看，不这样做的话
# 12 条客户会全部"同一秒创建"，演示效果和只有 1 条没有本质区别。
#
# 零强依赖——本组件是叶子，这个脚本本身就是 SOP-W-7"单独装一个组件也
# 有完整数据"要验证的最简单样板：`make -C components/mdm/customer seed`
# 只需要本组件自己的容器在跑，不需要任何别的组件。
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"

C_GRN=$'\033[32m'; C_RED=$'\033[31m'; C_OFF=$'\033[0m'
ok()  { echo "${C_GRN}✓${C_OFF} $*"; }
die() { echo "${C_RED}✗${C_OFF} $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"; }
need docker; need python3

NET="${BRICKKIT_NET:-brickkit-$(basename "$ROOT")-net}"
docker network inspect "$NET" >/dev/null 2>&1 || die "docker 网络 $NET 不存在——先把本组件 brickkit up 起来（整套或只装这一个）"

CNAME="$(docker ps --filter "name=${NET%-net}-mdm-customer-" --format '{{.Names}}' | head -1)"
[ -n "$CNAME" ] || die "mdm-customer 容器没在跑——先 brickkit up"

GRPC_PORT="$(awk -F'\t' '$2=="mdm/customer"{print $4}' "$ROOT/registry/ports.tsv")"
[ -n "$GRPC_PORT" ] || die "registry/ports.tsv 里找不到 mdm/customer 的 grpc 端口"

# 平台不把 gRPC 端口映射到宿主机（导读第 1/14 条），grpcurl 用容器镜像
# 加入 brickkit 网络直连，不是打 localhost。
GRPCURL="docker run --rm --network $NET -v $DIR/contracts:/contracts:ro fullstorydev/grpcurl:latest"
TARGET="$CNAME:$GRPC_PORT"

mkcustomer() {
  local key="$1" name="$2" credit="$3"
  $GRPCURL -plaintext -import-path /contracts -proto mdm/customer/v1/customer.proto \
    -d "{\"idempotency_key\":\"$key\",\"name\":\"$name\",\"credit_limit\":\"$credit\"}" \
    "$TARGET" mdm.customer.v1.CustomerService/Create \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["customer"]["id"])'
}

setstatus() {
  local key="$1" id="$2" status="$3"
  # SetStatus 要求当前 version——先 Get 一下拿最新值，乐观锁字段不能瞎填。
  local version
  version="$($GRPCURL -plaintext -import-path /contracts -proto mdm/customer/v1/customer.proto \
    -d "{\"id\":\"$id\"}" "$TARGET" mdm.customer.v1.CustomerService/Get \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')"
  $GRPCURL -plaintext -import-path /contracts -proto mdm/customer/v1/customer.proto \
    -d "{\"idempotency_key\":\"$key\",\"id\":\"$id\",\"version\":$version,\"status\":\"$status\"}" \
    "$TARGET" mdm.customer.v1.CustomerService/SetStatus >/dev/null
}

addcontact() {
  local key="$1" id="$2" name="$3" phone="$4" email="$5"
  $GRPCURL -plaintext -import-path /contracts -proto mdm/customer/v1/customer.proto \
    -d "{\"idempotency_key\":\"$key\",\"customer_id\":\"$id\",\"contact\":{\"name\":\"$name\",\"phone\":\"$phone\",\"email\":\"$email\",\"primary\":true}}" \
    "$TARGET" mdm.customer.v1.CustomerService/AddContact >/dev/null
}

echo "── mdm-customer：灌 12 个示例客户（1-5 是已被下游引用的固定契约，只增不改）──"
C1="$(mkcustomer seed-customer-1 "「本地测试」华南电子科技有限公司" 500000.00)"
addcontact seed-customer-1-contact "$C1" "张经理" "13800000001" "zhang@example.com"
C2="$(mkcustomer seed-customer-2 "「本地测试」京城机械制造集团" 300000.00)"
C3="$(mkcustomer seed-customer-3 "「本地测试」江南纺织实业公司" 200000.00)"
C4="$(mkcustomer seed-customer-4 "「本地测试」西部矿业贸易公司" 800000.00)"
C5="$(mkcustomer seed-customer-5 "「本地测试」滨海物流仓储公司（已停用样例）" 150000.00)"
setstatus seed-customer-5-disable "$C5" CUSTOMER_STATUS_DISABLED

# 6-12 是本轮新增，覆盖更多行业画像 + 信用额度从 0 到百万的完整区间。
C6="$(mkcustomer seed-customer-6 "「本地测试」都市零售连锁超市" 50000.00)"
C7="$(mkcustomer seed-customer-7 "「本地测试」云帆软件科技有限公司" 1000000.00)"
addcontact seed-customer-7-contact-1 "$C7" "李总" "13800000007" "li@example.com"
addcontact seed-customer-7-contact-2 "$C7" "王助理" "13800000107" "wang.assist@example.com"
# ⚠️ 信用额度刻意压得极低——crm-opportunity/erp-sales 补种子数据时，
# 拿这个客户建一个金额稍大的商机赢单，就能真机演示 ConfirmOrder 的
# TCC 链在信用超限分支上真的会拒绝（设计计划 §3.1 的 Saga 补偿场景，
# 阶段三 Task 14 验证过逻辑存在，但种子数据里从来没有一个"专门用来
# 演示这条路径"的客户）。
C8="$(mkcustomer seed-customer-8 "「本地测试」鲜达食品饮料公司（低信用额度样例）" 5000.00)"
C9="$(mkcustomer seed-customer-9 "「本地测试」恒基建筑工程公司" 600000.00)"
C10="$(mkcustomer seed-customer-10 "「本地测试」新能源电力集团" 900000.00)"
# 零信用额度 + 停用，两种边界叠一起的组合样例。
C11="$(mkcustomer seed-customer-11 "「本地测试」丰收农业发展公司（零额度+已停用样例）" 0.00)"
setstatus seed-customer-11-disable "$C11" CUSTOMER_STATUS_DISABLED
C12="$(mkcustomer seed-customer-12 "「本地测试」康泰医疗器械公司" 400000.00)"

ok "客户：$C1(ACTIVE+1联系人) $C2(ACTIVE) $C3(ACTIVE) $C4(ACTIVE) $C5(DISABLED) $C6(ACTIVE) $C7(ACTIVE+2联系人) $C8(ACTIVE,低额度) $C9(ACTIVE) $C10(ACTIVE) $C11(DISABLED,零额度) $C12(ACTIVE)"

# ── 时间跨度回填：让"最近 N 天"这类查询、列表默认按时间排序有真实的
# 新旧可看，不是全部客户都"刚刚创建"。customers 表不分区，直接 UPDATE
# 安全（不像 erp-inventory 的 inventory_movements 那样牵扯分区放置）。
# 只回填新增的 6-12（1-5 是既有契约数据，创建时间不动它）。
psqlx() { docker exec -i be-postgres psql -U postgres -d brickkit_db -v ON_ERROR_STOP=1 -q "$@"; }
psqlx <<SQL
SET search_path TO mdm_customer;
UPDATE customers SET created_at = now() - interval '4 months', updated_at = now() - interval '4 months' WHERE id = '$C6';
UPDATE customers SET created_at = now() - interval '3 months', updated_at = now() - interval '3 months' WHERE id = '$C7';
UPDATE customers SET created_at = now() - interval '2 months', updated_at = now() - interval '2 months' WHERE id = '$C9';
UPDATE customers SET created_at = now() - interval '1 months', updated_at = now() - interval '1 months' WHERE id = '$C10';
SQL
ok "已给 4 个客户回填历史创建时间（1-4 个月前），列表不再全部挤在同一秒"
