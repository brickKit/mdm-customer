#!/usr/bin/env bash
# 本组件自己的种子数据：5 个示例客户，覆盖 ACTIVE/DISABLED 两种状态 +
# 一条带联系人的样例（不止 happy path，总纲 SOP-W-7"完整度要求"）。
#
# ⚠️ 只给本地开发/演示用，不出现在任何部署/CI 流程里。数据都是假的，
# 灌进的是真实运行中的本组件数据库（用户已明确同意，见根仓库
# feedback_direct_db_seeding_ok 记录）。全程走真实 gRPC 调用，不是直接
# 写库——claim-first 幂等（固定 idempotency_key），重复跑不会重复建。
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

echo "── mdm-customer：灌 5 个示例客户 ──"
C1="$(mkcustomer seed-customer-1 "「本地测试」华南电子科技有限公司" 500000.00)"
addcontact seed-customer-1-contact "$C1" "张经理" "13800000001" "zhang@example.com"
C2="$(mkcustomer seed-customer-2 "「本地测试」京城机械制造集团" 300000.00)"
C3="$(mkcustomer seed-customer-3 "「本地测试」江南纺织实业公司" 200000.00)"
C4="$(mkcustomer seed-customer-4 "「本地测试」西部矿业贸易公司" 800000.00)"
C5="$(mkcustomer seed-customer-5 "「本地测试」滨海物流仓储公司（已停用样例）" 150000.00)"
setstatus seed-customer-5-disable "$C5" CUSTOMER_STATUS_DISABLED

ok "客户：$C1(ACTIVE+联系人) $C2(ACTIVE) $C3(ACTIVE) $C4(ACTIVE) $C5(DISABLED)"
