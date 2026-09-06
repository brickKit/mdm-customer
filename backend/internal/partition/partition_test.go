package partition

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMondayOf(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"2026-09-06", "2026-08-31"}, // Sunday -> 上一周一
		{"2026-08-31", "2026-08-31"}, // Monday -> 自己
		{"2026-09-02", "2026-08-31"}, // Wednesday -> 本周一
	}
	for _, c := range cases {
		in, err := time.Parse("2006-01-02", c.in)
		if err != nil {
			t.Fatal(err)
		}
		want, err := time.Parse("2006-01-02", c.want)
		if err != nil {
			t.Fatal(err)
		}
		got := mondayOf(in)
		if !got.Equal(want) {
			t.Errorf("mondayOf(%s) = %s，期望 %s", c.in, got.Format("2006-01-02"), c.want)
		}
	}
}

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

// TestEnsureAll_建当前周分区且连跑两次都成功 验证两件事：真的建出了
// 分区（不是只算出了名字没执行），以及幂等（迁移必须能连跑两次，
// §13.3 铁律五，分区维护同理——第二次跑到的应该已经是"分区存在，跳过"）。
func TestEnsureAll_建当前周分区且连跑两次都成功(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := ensureAll(ctx, db, "mdm_customer_rw", "mdm_customer"); err != nil {
		t.Fatalf("第一次执行失败：%v", err)
	}
	if err := ensureAll(ctx, db, "mdm_customer_rw", "mdm_customer"); err != nil {
		t.Fatalf("第二次执行失败（应该幂等）：%v", err)
	}

	weekStart := mondayOf(time.Now().UTC())
	name := "event_outbox_" + weekStart.Format("2006_01_02")
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('mdm_customer.'||$1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("期望分区 %s 已经建好", name)
	}
}
