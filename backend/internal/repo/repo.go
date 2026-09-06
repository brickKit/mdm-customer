// Package repo 是 mdm-customer 的数据访问层：customers/contacts/
// billing_infos 三张表 + Outbox 写入。跨组件读走 besdk.BatchGetRouted，
// List 的时间窗口走 besdk.ListWindow——这一层只管"接对了没有"，SDK 通用
// 逻辑本身的正确性由 be-sdk-go 自己的测试守（见 repo_test.go 顶部注释）。
package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		var code string
		var createdAt, updatedAt time.Time
		var version int64

		// ⚠️ RETURNING 里带上 credit_limit 并把它 Scan 回 creditLimit——不能
		// 直接把调用方传入的原始字符串塞回返回值：NUMERIC(18,2) 落库时会被
		// 规整成两位小数（"0" 存完读出来是 "0.00"），原样回传会让 Create
		// 的返回值与随后 Get 同一条记录时的格式不一致（L3 测试
		// TestCreate_creditLimit为0时允许 测出来的，之前的测试全传的是
		// 已经带两位小数的输入，没暴露过这条）。
		if in.Code == "" {
			// 留空则自动生成："C" + 6 位自增数字（设计计划 §9 待决问题 2）。
			// 用 customers_id_seq 的下一个值同时决定 id 与生成的 code——
			// 一次 nextval 定两样，不必插入之后再回改 code。显式插入 id，
			// 不再走列的 DEFAULT nextval（那个值已经在这里用掉了）。
			if err := tx.QueryRowContext(ctx, `SELECT nextval('customers_id_seq')`).Scan(&id); err != nil {
				return fmt.Errorf("生成客户编号: %w", err)
			}
			code = fmt.Sprintf("C%06d", id)
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO customers (id, code, name, tax_no, credit_limit)
				VALUES ($1, $2, $3, $4, $5)
				RETURNING created_at, updated_at, version, credit_limit`,
				id, code, in.Name, in.TaxNo, creditLimit,
			).Scan(&createdAt, &updatedAt, &version, &creditLimit); err != nil {
				return fmt.Errorf("insert customers: %w", err)
			}
		} else {
			// 显式传入：唯一索引 customers_code_uniq 本身就会在冲突时报错。
			code = in.Code
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO customers (code, name, tax_no, credit_limit)
				VALUES ($1, $2, $3, $4)
				RETURNING id, created_at, updated_at, version, credit_limit`,
				code, in.Name, in.TaxNo, creditLimit,
			).Scan(&id, &createdAt, &updatedAt, &version, &creditLimit); err != nil {
				return fmt.Errorf("insert customers: %w", err)
			}
		}
		idStr := strconv.FormatInt(id, 10)

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO command_idempotency (idempotency_key, command, result_id) VALUES ($1, $2, $3)`,
			in.IdempotencyKey, "Create", idStr); err != nil {
			return fmt.Errorf("insert command_idempotency: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"id": idStr, "code": code, "name": in.Name, "tax_no": in.TaxNo,
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
			ID: idStr, Code: code, Name: in.Name, TaxNo: in.TaxNo,
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
	c, err := scanCustomerRow(row)
	if err != nil {
		return nil, fmt.Errorf("查 customers: %w", err)
	}
	return c, nil
}

// rowScanner 是 *sql.Row 与 *sql.Rows 的公共部分——scanCustomerRow 两边
// 都要用（Update/SetStatus 的 RETURNING 走 QueryRowContext，BatchGet
// 走 QueryContext）。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanCustomerRow(row rowScanner) (*Customer, error) {
	var rawID int64
	var c Customer
	if err := row.Scan(&rawID, &c.Code, &c.Name, &c.TaxNo, &c.CreditLimit,
		&c.CreatedAt, &c.UpdatedAt, &c.Version, &c.Status); err != nil {
		return nil, err
	}
	c.ID = strconv.FormatInt(rawID, 10)
	return &c, nil
}

// ErrVersionConflict 是乐观锁冲突：请求带的 version 与库里当前值不一致。
// gRPC/HTTP 层把它映射成 Aborted/409（§4.6 幂等性铁律配套的并发控制）。
var ErrVersionConflict = errors.New("version 冲突：与库里当前值不一致")

// ErrNotFound：按 id 查不到。gRPC/HTTP 层映射成 NotFound/404。
var ErrNotFound = errors.New("not found")

// UpdateInput 对应 UpdateRequest。
type UpdateInput struct {
	IdempotencyKey string
	ID             string
	Version        int64 // 乐观锁：与库里不一致则拒绝
	Name           string
	TaxNo          string
	CreditLimit    string
}

func (r *Repo) Update(ctx context.Context, in UpdateInput) (*Customer, error) {
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

		row := tx.QueryRowContext(ctx, `
			UPDATE customers
			SET name = $1, tax_no = $2, credit_limit = $3, version = version + 1, updated_at = now()
			WHERE id = $4 AND version = $5
			RETURNING id, code, name, tax_no, credit_limit, created_at, updated_at, version, status`,
			in.Name, in.TaxNo, creditLimit, in.ID, in.Version)
		c, err := scanCustomerRow(row)
		if err == sql.ErrNoRows {
			return ErrVersionConflict
		}
		if err != nil {
			return fmt.Errorf("update customers: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO command_idempotency (idempotency_key, command, result_id) VALUES ($1, $2, $3)`,
			in.IdempotencyKey, "Update", c.ID); err != nil {
			return fmt.Errorf("insert command_idempotency: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"id": c.ID, "name": c.Name, "tax_no": c.TaxNo,
			"credit_limit": c.CreditLimit, "status": c.Status, "version": c.Version,
		})
		if err != nil {
			return err
		}
		if err := besdk.PublishOutbox(tx, r.schema, besdk.Event{
			Subject: "mdm.customer.updated.v1", AggregateID: c.ID, Version: c.Version, Payload: payload,
		}); err != nil {
			return err
		}

		out = c
		return nil
	})
	return out, err
}

// SetStatusInput 对应 SetStatusRequest。
//
// ⚠️ DISABLED 不是终态：允许流转回 ACTIVE（客户后续可能重新建立业务
// 关系），与设计计划 §7"不归档"一致——两个方向都只受乐观锁约束，没有
// 额外的状态机限制（设计计划 §3 的修正记录）。
type SetStatusInput struct {
	IdempotencyKey string
	ID             string
	Version        int64
	Status         string // "ACTIVE" | "DISABLED"
}

func (r *Repo) SetStatus(ctx context.Context, in SetStatusInput) (*Customer, error) {
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

		row := tx.QueryRowContext(ctx, `
			UPDATE customers
			SET status = $1, version = version + 1, updated_at = now()
			WHERE id = $2 AND version = $3
			RETURNING id, code, name, tax_no, credit_limit, created_at, updated_at, version, status`,
			in.Status, in.ID, in.Version)
		c, err := scanCustomerRow(row)
		if err == sql.ErrNoRows {
			return ErrVersionConflict
		}
		if err != nil {
			return fmt.Errorf("update customers: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO command_idempotency (idempotency_key, command, result_id) VALUES ($1, $2, $3)`,
			in.IdempotencyKey, "SetStatus", c.ID); err != nil {
			return fmt.Errorf("insert command_idempotency: %w", err)
		}

		// ⚠️ 事件清单（contracts/events/customer.events.json）只声明了
		// created/updated/disabled 三个 subject，没有 "enabled"——重新
		// 启用没有专门事件，复用 updated（它的 payload 本来就带 status
		// 字段）。不能顺手发明一个新 subject，那要先走一遍决策 19
		// （事件只增不删不改）的流程（设计计划 §9、service_test.go）。
		subject := "mdm.customer.updated.v1"
		if in.Status == "DISABLED" {
			subject = "mdm.customer.disabled.v1"
		}
		payload, err := json.Marshal(map[string]any{
			"id": c.ID, "name": c.Name, "tax_no": c.TaxNo,
			"credit_limit": c.CreditLimit, "status": c.Status, "version": c.Version,
		})
		if err != nil {
			return err
		}
		if err := besdk.PublishOutbox(tx, r.schema, besdk.Event{
			Subject: subject, AggregateID: c.ID, Version: c.Version, Payload: payload,
		}); err != nil {
			return err
		}

		out = c
		return nil
	})
	return out, err
}

// Contact 是 contacts 表的一行。
type Contact struct {
	ID      string
	Name    string
	Phone   string
	Email   string
	Primary bool
}

// AddContactInput 对应 AddContactRequest。
type AddContactInput struct {
	IdempotencyKey string
	CustomerID     string
	Name           string
	Phone          string
	Email          string
	Primary        bool
}

// AddContact 没有对应的事件——事件清单（events.json）里没有声明"联系人
// 新增"这个 subject，不能顺手发明一个（决策 19：事件只增不删不改，改动
// 要先走契约）。
func (r *Repo) AddContact(ctx context.Context, in AddContactInput) (*Contact, error) {
	var out *Contact
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		existingID, err := lookupIdempotency(ctx, tx, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if existingID != "" {
			c, err := getContactByID(ctx, tx, existingID)
			if err != nil {
				return err
			}
			out = c
			return nil
		}

		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO contacts (customer_id, name, phone, email, is_primary)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id`,
			in.CustomerID, in.Name, in.Phone, in.Email, in.Primary,
		).Scan(&id); err != nil {
			return fmt.Errorf("insert contacts: %w", err)
		}
		idStr := strconv.FormatInt(id, 10)

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO command_idempotency (idempotency_key, command, result_id) VALUES ($1, $2, $3)`,
			in.IdempotencyKey, "AddContact", idStr); err != nil {
			return fmt.Errorf("insert command_idempotency: %w", err)
		}

		out = &Contact{ID: idStr, Name: in.Name, Phone: in.Phone, Email: in.Email, Primary: in.Primary}
		return nil
	})
	return out, err
}

func getContactByID(ctx context.Context, tx *sql.Tx, id string) (*Contact, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, name, phone, email, is_primary FROM contacts WHERE id = $1`, id)
	var rawID int64
	var c Contact
	if err := row.Scan(&rawID, &c.Name, &c.Phone, &c.Email, &c.Primary); err != nil {
		return nil, fmt.Errorf("查 contacts: %w", err)
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
	c, err := scanCustomerRow(rows)
	if err != nil {
		return "", nil, err
	}
	return c.ID, c, nil
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
