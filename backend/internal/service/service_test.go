package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/mdm-customer/backend/internal/repo"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestService(t *testing.T) (*Service, *repo.Repo, *sql.DB) {
	t.Helper()
	db := testDB(t)
	r := repo.New(db, "mdm_customer_rw", "mdm_customer")
	return New(r, slog.Default()), r, db
}

func TestUpdate_版本不一致时拒绝(t *testing.T) {
	svc, r, _ := newTestService(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: "svc-update-001", Code: "C-SVC-UPDATE", Name: "旧名字"})
	if err != nil {
		t.Fatal(err)
	}

	// 乐观锁：version 与库里不一致则拒绝，不许写穿
	_, err = svc.Update(ctx, UpdateInput{
		IdempotencyKey: "svc-update-002", ID: c.ID, Version: c.Version + 1, Name: "新名字"})
	if err == nil {
		t.Fatal("version 不一致时应该拒绝，实际没报错")
	}

	got, _, err := r.BatchGet(ctx, []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "旧名字" {
		t.Fatalf("version 冲突时不该写穿，实际：%+v", got)
	}
}

// TestSetStatus_两个方向都允许流转 ⚠️ 没有照抄计划原文的
// "只允许单向演进"（DISABLED 是终态，ACTIVE→DISABLED 允许、
// DISABLED→ACTIVE 拒绝）——那与设计计划 §3 的修正记录矛盾：
// disabled 不是终态，客户后续可能重新建立业务关系，允许流转回 active
// （与设计计划 §7"不归档"的结论保持一致：真有终态才需要归档判据）。
// 以设计计划为准，这里改成两个方向都允许，各自只受乐观锁约束。
func TestSetStatus_两个方向都允许流转(t *testing.T) {
	svc, r, _ := newTestService(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: "svc-status-001", Code: "C-SVC-STATUS", Name: "状态流转测试"})
	if err != nil {
		t.Fatal(err)
	}

	disabled, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: "svc-status-002", ID: c.ID, Version: c.Version, Status: "DISABLED"})
	if err != nil {
		t.Fatalf("ACTIVE → DISABLED 应该允许：%v", err)
	}

	reactivated, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: "svc-status-003", ID: c.ID, Version: disabled.Version, Status: "ACTIVE"})
	if err != nil {
		t.Fatalf("DISABLED → ACTIVE 应该允许（不是终态），实际报错：%v", err)
	}
	if reactivated.Status != "ACTIVE" {
		t.Fatalf("期望状态回到 ACTIVE，实际 %q", reactivated.Status)
	}
}

func TestSetStatus_停用时发出disabled事件(t *testing.T) {
	svc, r, db := newTestService(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: "svc-event-001", Code: "C-SVC-EVENT", Name: "事件测试"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: "svc-event-002", ID: c.ID, Version: c.Version, Status: "DISABLED"})
	if err != nil {
		t.Fatal(err)
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(
				`SELECT count(*) FROM event_outbox
					WHERE subject = 'mdm.customer.disabled.v1' AND aggregate_id = $1 AND version >= $2`,
				c.ID, updated.Version).Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("期望落 1 条 mdm.customer.disabled.v1，实际 %d 条", n)
	}
}

// TestSetStatus_重新启用时发出updated事件而不是专门事件 ⚠️ 补的测试，不在
// 计划原文里——但直接由上面那条"两个方向都允许"的修正带出来：事件清单
// （contracts/events/customer.events.json）里只声明了 created/updated/
// disabled 三个 subject，没有 "enabled"。重新启用没有自己的专门事件，
// 复用 updated（它的 payload 本来就带 status 字段）——不能在实现时顺手
// 发明一个新 subject，那要先回契约与设计计划走一遍决策 19（事件只增不删
// 不改）的流程。
func TestSetStatus_重新启用时发出updated事件而不是专门事件(t *testing.T) {
	svc, r, db := newTestService(t)
	ctx := context.Background()

	c, err := r.Create(ctx, repo.CreateInput{
		IdempotencyKey: "svc-reenable-001", Code: "C-SVC-REENABLE", Name: "重新启用测试"})
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: "svc-reenable-002", ID: c.ID, Version: c.Version, Status: "DISABLED"})
	if err != nil {
		t.Fatal(err)
	}
	reactivated, err := svc.SetStatus(ctx, SetStatusInput{
		IdempotencyKey: "svc-reenable-003", ID: c.ID, Version: disabled.Version, Status: "ACTIVE"})
	if err != nil {
		t.Fatal(err)
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(
				`SELECT count(*) FROM event_outbox
					WHERE subject = 'mdm.customer.updated.v1' AND aggregate_id = $1 AND version >= $2`,
				c.ID, reactivated.Version).Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("重新启用应该发 mdm.customer.updated.v1（不是专门的 enabled 事件），实际匹配 %d 条", n)
	}
}

// ⚠️ L3 补充测试（Task 16 步骤 3.5）。计划原文给的例子是"Create 传空
// code → InvalidArgument"，但 code 留空是设计计划 §9 定的自动编号触发
// 条件（见 repo_test.go 的 TestCreate_code留空时自动生成），不是错误——
// 那条例子本身与已经确认的设计矛盾（自查第 0 条：L2 结论挡路几乎总是
// 例子/理解错了，不是反过来）。改验真正没有兜底的必填字段 name。
func TestCreate_name为空时拒绝且不落库(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, repo.CreateInput{
		IdempotencyKey: "svc-l3-name-empty", Code: "C-L3-NAME-EMPTY", Name: ""})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("name 为空应该拒绝并返回 ErrInvalidArgument，实际：%v", err)
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM customers WHERE code = $1`, "C-L3-NAME-EMPTY").Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("校验应该发生在落库之前，实际库里已经有 %d 条", n)
	}
}

func TestCreate_creditLimit为负数时拒绝(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, repo.CreateInput{
		IdempotencyKey: "svc-l3-credit-neg", Code: "C-L3-CREDIT-NEG", Name: "负数额度测试", CreditLimit: "-1"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("credit_limit 为负数应该拒绝并返回 ErrInvalidArgument，实际：%v", err)
	}
}

// TestCreate_creditLimit为0时允许 是上面那条的边界对照组：0 是合法值
// （新客户还没有信用额度是正常状态），不能因为校验"不能为负"顺手把 0
// 也拦下来。
func TestCreate_creditLimit为0时允许(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	c, err := svc.Create(ctx, repo.CreateInput{
		IdempotencyKey: "svc-l3-credit-zero", Code: "C-L3-CREDIT-ZERO", Name: "零额度测试", CreditLimit: "0"})
	if err != nil {
		t.Fatalf("credit_limit 为 0 应该允许，实际报错：%v", err)
	}
	if c.CreditLimit != "0.00" {
		t.Fatalf("期望落库后是 0.00，实际 %q", c.CreditLimit)
	}
}
