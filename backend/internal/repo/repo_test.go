package repo

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
	_ "github.com/jackc/pgx/v5/stdlib" // §12.4：不用 lib/pq，驱动名注册为 "pgx"
)

// ⚠️ 计划原文这里写的是 lib/pq——那个驱动已经在 be-sdk-go 的 Task 7 里
// 确认踩过坑并改掉了（§12.4 锁定表明确禁止），这里跟着 be-sdk-go 统一用
// pgx/v5/stdlib，不重蹈覆辙。
//
// ⚠️ 计划原文还给了一个 health_test.go（NewHealthHandler，签名不接受任何
// 依赖参数）。没有照抄：be-sdk-go 的 NewGinEngine 已经挂了 /healthz（零
// 依赖、恒 200），且 be-sdk-go 自己的 TestNewGinEngine_healthz不查依赖
// 已经覆盖了这条断言。组件自己再写一个等价的 handler 只是重复测试同一件
// 事，而且如果真去注册它，会和 NewGinEngine 已经注册的 /healthz 路由冲突
// （Gin 对同一 method+path 重复注册会直接 panic）。

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

// ⚠️ 补的测试，不在计划原文里：设计计划 §9 待决问题 2 定的规则是
// "code 留空则自动生成 C + 6 位自增数字"，但这条规则只写在设计文档和
// 字段注释里，一直没有真正的测试守着——写代码时才发现 Create 压根没
// 实现这段逻辑，只是把空字符串原样插进了 NOT NULL 列。
func TestCreate_code留空时自动生成(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	c, err := r.Create(ctx, CreateInput{IdempotencyKey: "test-autocode-001", Name: "自动编号客户"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Code == "" {
		t.Fatal("code 留空时应该自动生成，实际还是空字符串")
	}
	if !strings.HasPrefix(c.Code, "C") || len(c.Code) != 7 {
		t.Fatalf(`期望形如 "C" + 6 位数字，实际得到 %q`, c.Code)
	}
}

func TestCreate_幂等(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	in := CreateInput{IdempotencyKey: "test-idem-001", Code: "C-001",
		Name: "Acme", CreditLimit: "100000.00"}
	a, err := r.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// 同一个 idempotency_key 再来一次：不许新建，必须返回同一条
	b, err := r.Create(ctx, in)
	if err != nil {
		t.Fatalf("幂等重试报错了：%v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("幂等失效：第一次 %s，第二次 %s", a.ID, b.ID)
	}
}

// TestCreate_事件与业务数据同事务 验证 §3.10 的 Outbox Pattern：业务写入
// 与 PublishOutbox 必须在同一事务里，任一方失败两边都不许留下痕迹。
//
// ⚠️ 没有照抄计划原文的 r.CreateWithInjectedFailure——那等于往生产代码的
// 公开 API 上永久挂一个只为测试存在的方法，其他组件抄这份骨架时会把它
// 当成正常方法一起抄走。改用本包内部的 testHookAfterOutbox（未导出，见
// repo.go）：只有同包的 _test.go 摸得到，编译进生产二进制里是个永远为 nil
// 的变量，不占公开 API 一个字。
func TestCreate_事件与业务数据同事务(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	testHookAfterOutbox = func() error { return errors.New("注入的失败，验证回滚") }
	t.Cleanup(func() { testHookAfterOutbox = nil })

	_, err := r.Create(ctx, CreateInput{
		IdempotencyKey: "test-rollback-001", Code: "C-ROLLBACK", Name: "Rollback"})
	if err == nil {
		t.Fatal("期望注入的失败被返回")
	}

	var n int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(
				`SELECT count(*) FROM event_outbox WHERE aggregate_id IN
					(SELECT id::text FROM customers WHERE code = $1)`,
				"C-ROLLBACK").Scan(&n)
		}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("业务回滚了但 outbox 留了 %d 条——说明事件不在同一事务里（§3.10）", n)
	}

	var m int
	if err := besdk.WithTx(ctx, db, "mdm_customer_rw", "mdm_customer",
		func(tx *sql.Tx) error {
			return tx.QueryRow(
				`SELECT count(*) FROM customers WHERE code = $1`, "C-ROLLBACK").Scan(&m)
		}); err != nil {
		t.Fatal(err)
	}
	if m != 0 {
		t.Fatalf("期望业务行也回滚，实际还留着 %d 条", m)
	}
}

// ⚠️ 补的测试，不在计划原文里：AddContact 的 customer_id 是从
// Customer.ID（string）传下来的，插的是 contacts.customer_id（bigint）
// 列——之前在 BatchGet 上怀疑过 string 参数绑定 bigint 列会报类型不匹配
// （查证后发现是自己manual psql PREPARE 的方式不对，pgx 实际按上下文推断
// 参数类型，不会报错），这里顺手用真库验证 AddContact 这条路径同样没事，
// 不必每次都靠"应该没问题"的推断。
func TestAddContact_customer_id按字符串传也能正常插入(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := New(db, "mdm_customer_rw", "mdm_customer")

	c, err := r.Create(ctx, CreateInput{IdempotencyKey: "test-contact-cust-001", Name: "联系人测试客户"})
	if err != nil {
		t.Fatal(err)
	}

	contact, err := r.AddContact(ctx, AddContactInput{
		IdempotencyKey: "test-contact-001", CustomerID: c.ID,
		Name: "张三", Phone: "13800000000", Primary: true,
	})
	if err != nil {
		t.Fatalf("AddContact 不该报错：%v", err)
	}
	if contact.ID == "" || contact.Name != "张三" {
		t.Fatalf("期望拿到新联系人，实际：%+v", contact)
	}

	// 幂等：同一个 idempotency_key 再来一次，不许新建
	again, err := r.AddContact(ctx, AddContactInput{
		IdempotencyKey: "test-contact-001", CustomerID: c.ID,
		Name: "张三", Phone: "13800000000", Primary: true,
	})
	if err != nil {
		t.Fatalf("幂等重试报错了：%v", err)
	}
	if again.ID != contact.ID {
		t.Fatalf("幂等失效：第一次 %s，第二次 %s", contact.ID, again.ID)
	}
}

// TestBatchGet_缺失的id不报错 测的不是 besdk.BatchGetRouted 本身（那是
// be-sdk-go 自己的 TestBatchGetRouted_热表命中_归档缺失都覆盖 在管）——
// 这里测的是 mdm-customer 把它接对了：用真的 customers 表、真的 scan
// 函数，缺失的 id 确实不报错、确实被分类进 missing。
func TestBatchGet_缺失的id不报错(t *testing.T) {
	db := testDB(t)
	r := New(db, "mdm_customer_rw", "mdm_customer")
	got, missing, err := r.BatchGet(context.Background(), []string{"999999999"})
	if err != nil {
		t.Fatalf("BatchGet 对缺失 id 不该报错：%v", err)
	}
	if len(got) != 0 || len(missing) != 1 {
		t.Fatalf("期望 0 命中 1 缺失，得到 %d/%d", len(got), len(missing))
	}
}

// TestList_未传时间范围时自动注入90天窗口 测的不是 besdk.ListWindow 本身
// （那是 be-sdk-go 自己的 TestListWindow_未传时间范围时自动注入最近90天
// 在管）——这里测的是 mdm-customer 的 List 真的调用了它，不是自己拼了一套
// 平行逻辑。buildListQuery 未导出，只在包内可见，不需要真连库就能测。
func TestList_未传时间范围时自动注入90天窗口(t *testing.T) {
	q := buildListQuery(ListInput{PageSize: 20}) // 不传 CreatedAfter/CreatedBefore
	if q.From.IsZero() || q.To.IsZero() {
		t.Fatal("框架层必须自动注入默认时间窗口，否则用户无条件查询会拖垮数据库（§11.4.1）")
	}
	days := q.To.Sub(q.From).Hours() / 24
	if days < 89 || days > 91 {
		t.Fatalf("默认窗口应约为 90 天，实际 %.1f 天", days)
	}
}
