// migrate 是全拆态迁移容器的入口（合并态由外壳读 Module.Migrations 执行，
// 平台不为 local: true 的组件生成迁移容器，§13.3 铁律五）。
//
// 它在 backend/cmd/ 下，属于「装配」不是「模块」，module-check 的排除范围
// 就是 backend/cmd/——这里允许读 os.Getenv（§12.5 的零 os.Getenv 只管
// backend/module/ 与 backend/internal/）。
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	schema := os.Getenv("PG_SCHEMA")
	if schema == "" {
		schema = "mdm_customer"
	}
	// 平台注入的 DATABASE_* 是保留前缀，只读不写（§C）
	//
	// ⚠️ 实测踩坑：x-migrations-table 不认 "schema.table" 写法——golang-migrate
	// 的 postgres 驱动只有在同时给 x-migrations-table-quoted=true 且值形如
	// `"schema"."table"` 时才会拆开 schema 与表名；不加那个开关时，整个字符串
	// 被当成*一个*字面表名，"mdm_customer.schema_migrations_mdm_customer" 会
	// 变成一张名字里带着字面点号的表（虽然还是落在 search_path 指向的
	// schema 里，因为 schema 是靠 search_path 让 CURRENT_SCHEMA() 决定的，
	// 跟 x-migrations-table 无关）。正确写法是 x-migrations-table 只给裸表名，
	// schema 完全交给 search_path 一个参数负责。
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable"+
			// ⚠️ 两条都必须有（§11.2.3）：
			//   search_path            —— 迁移里的 SQL、以及迁移状态表本身
			//                             都落在本组件 schema
			//   x-migrations-table     —— 表名含组件标识，裸名不带 schema 前缀
			"&search_path=%s"+
			"&x-migrations-table=schema_migrations_mdm_customer",
		os.Getenv("DATABASE_USER"), os.Getenv("DATABASE_PASSWORD"),
		os.Getenv("DATABASE_HOST"), os.Getenv("DATABASE_PORT"),
		os.Getenv("DATABASE_NAME"), schema,
	)

	m, err := migrate.New("file://migrations", dsn)
	if err != nil {
		log.Fatalf("迁移初始化失败：%v", err)
	}
	if len(os.Args) > 1 && os.Args[1] == "down" {
		if err := m.Down(); err != nil && err != migrate.ErrNoChange {
			log.Fatalf("迁移回滚失败：%v", err)
		}
		return
	}
	// ⚠️ ErrNoChange 不是错误。迁移必须能连跑两次都成功：
	//    合并态由外壳跑、全拆态由平台跑，两条路径都要能重跑（§13.3 铁律五）
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("迁移失败：%v", err)
	}
	log.Println("迁移完成")
}
