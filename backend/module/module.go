// Package module 是 mdm-customer 唯一的装配入口（全局约束 §K、设计书
// §12.5.1、§13.3 铁律七）。单跑与合并走同一个 New 函数；模块只交回零件
// （handler、gRPC 注册函数、迁移、后台循环），谁去 Listen、谁开池、
// 谁 init OTel、谁装信号处理器，全归调用方。
//
// SOP-P 判过：这个组件是只读枢纽 + 常规 CRUD，不用任何设计模式，直接写
// 就是最清楚的（见 docs/手册.md 的"项目结构"一节）。
package module

import (
	"context"

	besdk "github.com/brickKit/be-sdk-go"
	customerv1 "github.com/brickKit/mdm-customer/gen/mdm/customer/v1"
	"google.golang.org/grpc"

	grpcapi "github.com/brickKit/mdm-customer/backend/internal/grpc"
	httpapi "github.com/brickKit/mdm-customer/backend/internal/http"
	"github.com/brickKit/mdm-customer/backend/internal/partition"
	"github.com/brickKit/mdm-customer/backend/internal/repo"
	"github.com/brickKit/mdm-customer/backend/internal/service"
	"github.com/brickKit/mdm-customer/migrations"
)

// New 构造 mdm-customer 模块。签名一个字都不许改（§12.5.1）——62 个
// 组件都是这一个签名，外壳启动器与 be-ops 产出 4 都按它生成。
func New(ctx context.Context, rt *besdk.Runtime) (*besdk.Module, error) {
	// ⚠️ 配置只从 rt.Config 来，模块里零 os.Getenv（§12.5.3、决策 110）。
	// 一个进程只有一份 environ：合并后 22 个模块的 PG_SCHEMA 会互相顶掉，
	// 不报错，模块按别人的 schema 建表写数据。
	schema := rt.Config.StringOr("pgSchema", "mdm_customer")
	role := schema + "_rw"

	// ⚠️ 池从 rt.DB 来，不许自己 sql.Open（§13.3 铁律二）。
	// ⚠️ OTel / 日志 / 指标 registry 也从 rt 来，不许自己 init（§12.5.2）——
	// SetTracerProvider 是「最后一个 init 的赢」，而症状是一路全绿。
	r := repo.New(rt.DB, role, schema)
	svc := service.New(r, rt.Logger)

	// HTTP：engine 必须用 besdk.NewGinEngine，它已挂好 OTel / request-id /
	// error→status / PII 脱敏日志 / RED 指标 / /healthz / /metrics。
	// 自己 gin.New() 不会报错，只是这个组件从此没有 trace（§12.5.2）。
	eng := besdk.NewGinEngine(rt)
	httpapi.RegisterRoutes(eng, svc)

	return &besdk.Module{
		HTTPHandler: eng,

		// ⚠️ gRPC 一个不省，而且由调用方在 extraPorts["grpc"] 上 Listen。
		// 合并进外壳后，同一个 Go 进程里的两个模块仍然必须通过 gRPC 互相
		// 调用，不许直接函数调用——gRPC 是逻辑边界的物理载体，省掉它等于
		// 合并那一刻边界消失，再也拆不回去（§1.5 原则一）。
		RegisterGRPC: func(gs *grpc.Server) {
			customerv1.RegisterCustomerServiceServer(gs, grpcapi.New(svc))
		},

		Migrations: migrations.FS, // 合并态由外壳按拓扑顺序跑（§13.3 铁律五）

		// 后台循环：Outbox 推送 + 分区自动维护（决策 54：跨月/跨周时分区
		// 不存在会写崩）。⚠️ 两个循环必须并发跑，不能顺序调用——
		// StartOutboxPump 是阻塞到 ctx 取消才返回的循环，顺序写会让第二个
		// 循环永远等不到执行机会（计划原文的模板漏了这一点）。
		Start: func(ctx context.Context) error {
			errCh := make(chan error, 2)
			go func() { errCh <- besdk.StartOutboxPump(ctx, rt.DB, schema, rt.NATS) }()
			go func() { errCh <- partition.Start(ctx, rt.DB, role, schema, rt.Logger) }()

			select {
			case <-ctx.Done():
				return nil
			case err := <-errCh:
				return err // ⚠️ 返回 error，不许 log.Fatal：一个模块退进程 = 整组组件一起没了
			}
		},
		Stop: func(ctx context.Context) error { return nil }, // 后台循环靠 ctx 退出
	}, nil
}
