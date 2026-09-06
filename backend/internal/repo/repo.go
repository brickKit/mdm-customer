// Package repo 是 mdm-customer 的数据访问层：customers/contacts/
// billing_infos 三张表 + Outbox 写入。跨组件读走 besdk.BatchGetRouted，
// List 的时间窗口走 besdk.ListWindow——这一层只管"接对了没有"，SDK 通用
// 逻辑本身的正确性由 be-sdk-go 自己的测试守（见 repo_test.go 顶部注释）。
package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// Customer 是 customers 表的一行。CreditLimit 一律用 string 传 decimal
// ——double 跨语言序列化会丢精度（设计计划 §3）。
type Customer struct {
	ID          string
	Code        string
	Name        string
	TaxNo       string
	CreditLimit string
	Status      string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CreateInput 对应 CreateRequest（contracts/mdm/customer/v1/customer.proto）。
type CreateInput struct {
	IdempotencyKey string
	Code           string // 留空则自动生成 "C" + 6 位自增数字（设计计划 §9）
	Name           string
	TaxNo          string
	CreditLimit    string
}

// Repo 持有共享池 + 本组件的 role/schema，写操作一律经 besdk.WithTx 切换。
type Repo struct {
	db     *sql.DB
	role   string
	schema string
}

func New(db *sql.DB, role, schema string) *Repo {
	return &Repo{db: db, role: role, schema: schema}
}

// testHookAfterOutbox 仅供本包测试用（未导出，其他包摸不到）：Create 在
// PublishOutbox 成功之后、提交事务之前调它——测试借此验证"业务数据 +
// 事件必须同事务"，不必往公开 API 上挂一个永久的故障注入方法
// （见 repo_test.go 的 TestCreate_事件与业务数据同事务）。
var testHookAfterOutbox func() error

// Create 幂等：同一个 idempotency_key 重试返回同一条，不新建
// （command_idempotency 唯一约束去重，§4.6）。
func (r *Repo) Create(ctx context.Context, in CreateInput) (*Customer, error) {
	var out *Customer
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		existingID, err := lookupIdempotency(ctx, tx, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if existingID != "" {
			c, err := getByID(ctx, tx, existingID)
			if err != nil {
				return err
			}
			out = c
			return nil
		}

		creditLimit := in.CreditLimit
		if creditLimit == "" {
			creditLimit = "0"
		}

		var id int64
		var createdAt, updatedAt time.Time
		var version int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO customers (code, name, tax_no, credit_limit)
			VALUES ($1, $2, $3, $4)
			RETURNING id, created_at, updated_at, version`,
			in.Code, in.Name, in.TaxNo, creditLimit,
		).Scan(&id, &createdAt, &updatedAt, &version); err != nil {
			return fmt.Errorf("insert customers: %w", err)
		}
		idStr := strconv.FormatInt(id, 10)

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO command_idempotency (idempotency_key, command, result_id) VALUES ($1, $2, $3)`,
			in.IdempotencyKey, "Create", idStr); err != nil {
			return fmt.Errorf("insert command_idempotency: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"id": idStr, "code": in.Code, "name": in.Name, "tax_no": in.TaxNo,
			"credit_limit": creditLimit, "status": "ACTIVE", "version": version,
		})
		if err != nil {
			return err
		}
		if err := besdk.PublishOutbox(tx, r.schema, besdk.Event{
			Subject: "mdm.customer.created.v1", AggregateID: idStr, Version: version, Payload: payload,
		}); err != nil {
			return err
		}

		if testHookAfterOutbox != nil {
			if err := testHookAfterOutbox(); err != nil {
				return err
			}
		}

		out = &Customer{
			ID: idStr, Code: in.Code, Name: in.Name, TaxNo: in.TaxNo,
			CreditLimit: creditLimit, Status: "ACTIVE", Version: version,
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		}
		return nil
	})
	return out, err
}

// lookupIdempotency 返回空字符串表示没查到（不是错误）。
func lookupIdempotency(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var resultID string
	err := tx.QueryRowContext(ctx,
		`SELECT result_id FROM command_idempotency WHERE idempotency_key = $1`, key).Scan(&resultID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("查 command_idempotency: %w", err)
	}
	return resultID, nil
}

func getByID(ctx context.Context, tx *sql.Tx, id string) (*Customer, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, code, name, tax_no, credit_limit, created_at, updated_at, version, status
			FROM customers WHERE id = $1`, id)
	var rawID int64
	var c Customer
	if err := row.Scan(&rawID, &c.Code, &c.Name, &c.TaxNo, &c.CreditLimit,
		&c.CreatedAt, &c.UpdatedAt, &c.Version, &c.Status); err != nil {
		return nil, fmt.Errorf("查 customers: %w", err)
	}
	c.ID = strconv.FormatInt(rawID, 10)
	return &c, nil
}

// BatchGet 是 gRPC BatchGet 的实现——BFF 层防 N+1 的唯一合法调用方式
// （§3.8）。冷热路由（热表 customers，缺失再查 customers_archive）交给
// besdk.BatchGetRouted；这里只管接对 schema/table/scan 函数。
func (r *Repo) BatchGet(ctx context.Context, ids []string) (found []*Customer, missing []string, err error) {
	var rows []*Customer
	err = besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		got, err := besdk.BatchGetRouted(ctx, tx, r.schema, "customers", ids, scanCustomer)
		if err != nil {
			return err
		}
		rows = got
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	foundIDs := make(map[string]bool, len(rows))
	for _, c := range rows {
		foundIDs[c.ID] = true
	}
	var missingIDs []string
	for _, id := range ids {
		if !foundIDs[id] {
			missingIDs = append(missingIDs, id)
		}
	}
	return rows, missingIDs, nil
}

func scanCustomer(rows *sql.Rows) (string, *Customer, error) {
	var rawID int64
	var c Customer
	if err := rows.Scan(&rawID, &c.Code, &c.Name, &c.TaxNo, &c.CreditLimit,
		&c.CreatedAt, &c.UpdatedAt, &c.Version, &c.Status); err != nil {
		return "", nil, err
	}
	c.ID = strconv.FormatInt(rawID, 10)
	return c.ID, &c, nil
}

// ListInput 对应 ListRequest。刻意没有 offset 字段——深分页在契约层面就
// 不可表达（决策 53）。
type ListInput struct {
	Cursor        string
	PageSize      int
	StatusFilter  string
	CreatedAfter  time.Time
	CreatedBefore time.Time
}

type ListResult struct {
	Customers  []*Customer
	NextCursor string
}

// buildListQuery 把 ListInput 接到 besdk.ListWindow——90 天默认窗口与
// 分页上限的算法本身是 SDK 的事，这里只负责别漏接（未导出，供
// repo_test.go 不连库就能验证接对了）。
func buildListQuery(in ListInput) besdk.Query {
	return besdk.ListWindow(besdk.Query{
		From:   in.CreatedAfter,
		To:     in.CreatedBefore,
		Cursor: in.Cursor,
		Limit:  in.PageSize,
	})
}

// cursor 编码 (created_at, id)：keyset 分页，不是 offset（决策 53）。
type cursorKey struct {
	CreatedAt time.Time
	ID        int64
}

func (r *Repo) List(ctx context.Context, in ListInput) (*ListResult, error) {
	q := buildListQuery(in)

	var ck *cursorKey
	if q.Cursor != "" {
		decoded, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, fmt.Errorf("非法 cursor：%w", err)
		}
		ck = &decoded
	}

	var out ListResult
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		query := `SELECT id, code, name, tax_no, credit_limit, created_at, updated_at, version, status
			FROM customers
			WHERE created_at >= $1 AND created_at <= $2`
		args := []any{q.From, q.To}
		if in.StatusFilter != "" {
			args = append(args, in.StatusFilter)
			query += fmt.Sprintf(" AND status = $%d", len(args))
		}
		if ck != nil {
			args = append(args, ck.CreatedAt, ck.ID)
			query += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
		}
		args = append(args, q.Limit+1) // 多取一条，用来判断是否还有下一页
		query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("查 customers: %w", err)
		}
		defer rows.Close()

		var customers []*Customer
		for rows.Next() {
			_, c, err := scanCustomer(rows)
			if err != nil {
				return err
			}
			customers = append(customers, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		if len(customers) > q.Limit {
			last := customers[q.Limit-1]
			var lastRawID int64
			lastRawID, err = strconv.ParseInt(last.ID, 10, 64)
			if err != nil {
				return err
			}
			out.NextCursor = encodeCursor(cursorKey{CreatedAt: last.CreatedAt, ID: lastRawID})
			customers = customers[:q.Limit]
		}
		out.Customers = customers
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
