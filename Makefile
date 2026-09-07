IMAGE   := brickenterprise/mdm-customer
VERSION := $(shell grep -E '^\s+version:' component.yaml | head -1 | awk '{print $$2}')

.PHONY: all check-version test image migrate-idempotent dag-check contract-check import-scan module-check docs-check smoke

all: check-version test image migrate-idempotent dag-check contract-check import-scan module-check docs-check

check-version:  ## component.yaml 的 version 与 git tag 不许分叉（§9.1 两个真相源）
	@tag="$$(git describe --tags --exact-match 2>/dev/null || true)"; \
	 if [ -n "$$tag" ] && [ "$$tag" != "v$(VERSION)" ]; then \
	   echo "✗ git tag $$tag 与 component.yaml 的 $(VERSION) 不一致"; exit 1; fi; \
	 echo "✓ version=$(VERSION)"

test:  ## 需要 TEST_PG_DSN 与 TEST_NATS_URL（可选，缺省走 nats.DefaultURL）
	go test ./... -race -count=1

image:
	docker build -t $(IMAGE):$(VERSION) .
	@# 镜像里必须有 /bin/sh + wget，否则平台的 CMD-SHELL 健康检查永远失败，
	@# 症状是「组件日志写着已就绪，而平台说它不健康」（§12.3.7）
	@docker run --rm --entrypoint sh $(IMAGE):$(VERSION) -c 'wget --version >/dev/null' \
	  && echo "✓ 镜像里有 sh + wget"

migrate-idempotent:  ## 同一份迁移连跑两次都必须成功（§13.3 铁律五）
	@# 需要 DATABASE_HOST/PORT/USER/PASSWORD/NAME + PG_SCHEMA（平台真实注入
	@# 的分离变量契约，与上面 test 用的单个 TEST_PG_DSN 是两套不同约定——
	@# 迁移是平台生成的独立容器在跑，模拟的必须是平台自己的注入形态）。
	@# 本地跑迁移要用 postgres 超级用户（不是 mdm_customer_rw）：分区维护
	@# 之类的 DDL 需要建表权限，平台生成的迁移容器同样用管理凭据连库。
	@go build -o /tmp/migrate-probe ./backend/cmd/migrate
	@/tmp/migrate-probe up && /tmp/migrate-probe up && echo "✓ 迁移幂等"

dag-check:  ## 强依赖图无环（§4.2）。mdm 是只读枢纽，依赖为空，天然无环
	@# ⚠️ 没有照抄计划原文的 "grep -A5 'components:' | grep -c '^\s*-\s'"——
	@# component.yaml 实际写法是同行内联 "components: []"，-A5 的向后看窗口
	@# 会把 dependencies.resources 列表的 "- { kind: ... }" 一起数进去，
	@# 假阳性报出 2 条依赖（实测：本该是 0）。改成直接判内联空数组。
	@if ! grep -qE '^\s*components:\s*\[\]' component.yaml; then \
	   echo "✗ mdm 是只读枢纽，dependencies.components 必须为空（§2.6）"; exit 1; fi
	@echo "✓ 无强依赖，无环"

contract-check:  ## 禁破坏性变更（§8.5、决策 33）
	buf lint
	buf breaking --against '.git#branch=main'

import-scan:  ## 铁律六：不许 import 任何其他组件仓库（§13.3）
	@bad="$$(go list -deps ./... 2>/dev/null | grep -E '^github.com/brickKit/' \
	         | grep -vE '^github.com/brickKit/(mdm-customer|be-sdk-go)(/|$$)' || true)"; \
	 if [ -n "$$bad" ]; then \
	   echo "✗ 铁律六违规，import 了其他组件仓库："; echo "$$bad"; exit 1; fi; \
	 echo "✓ 无组件间 import"

module-check:  ## 铁律七：模块能被合进外壳（§12.5、§13.3 铁律七）
	@# ⚠️ 以下几条只扫 backend/module 与 backend/internal（能被合进外壳的
	@# 部分）：backend/cmd 是装配用途，允许 os.Getenv（各 cmd 文件顶部有
	@# 注释说明），且 cmd/migrate 依赖 golang-migrate 自带的 postgres 驱动
	@# （内部用 lib/pq，是它自己的实现细节，不是本组件业务代码选的驱动，
	@# 只在一次性迁移容器里跑，从不会被合进外壳）——banned-libs 检查若不
	@# 缩小范围到 module+internal，会把这条无关依赖也扫出来。
	@#
	@# 检查前用 sed 去掉行内 // 注释再 grep：这个代码库的风格本来就大量
	@# 写"⚠️ 不许 X"这类解释性注释，逐字扫源码行不去注释，那些注释自己
	@# 提到的被禁 API 名字会把自己判成违规（实测：module.go 里解释"为什么
	@# 不许 log.Fatal"的那句注释，原样扫会命中 log.Fatal 这个词本身）。
	@# 同时排除 _test.go：连库测试用 os.Getenv("TEST_PG_DSN")/sql.Open 是
	@# 测试基础设施，不是模块运行时代码。
	@grep -qE 'func New\(ctx context\.Context, rt \*besdk\.Runtime\) \(\*besdk\.Module, error\)' \
	   backend/module/module.go || { echo "✗ module.New 签名与 §12.5.1 不一致"; exit 1; }
	@bad=""; \
	 for f in $$(find backend/module backend/internal -name '*.go' ! -name '*_test.go'); do \
	   hit="$$(sed 's://.*::' "$$f" | grep -nE 'os\.Getenv|os\.LookupEnv')"; \
	   [ -n "$$hit" ] && bad="$$bad$$f: $$hit\n"; \
	 done; \
	 if [ -n "$$bad" ]; then \
	   echo "✗ 模块代码里读了进程环境变量（22 个模块会互相顶掉，不报错）："; \
	   printf '%b' "$$bad"; exit 1; fi
	@bad=""; \
	 for f in $$(find backend/module backend/internal -name '*.go' ! -name '*_test.go'); do \
	   hit="$$(sed 's://.*::' "$$f" | grep -nE 'log\.Fatal|os\.Exit|signal\.Notify|otel\.SetTracerProvider|promauto\.|prometheus\.MustRegister|gin\.New\(|gin\.Default\(|sql\.Open')"; \
	   [ -n "$$hit" ] && bad="$$bad$$f: $$hit\n"; \
	 done; \
	 if [ -n "$$bad" ]; then \
	   echo "✗ 模块碰了进程级的东西或自己装配（§12.5.2）："; printf '%b' "$$bad"; exit 1; fi
	@bad="$$(go list -deps ./backend/module/... ./backend/internal/... 2>/dev/null \
	         | grep -E 'labstack/echo|gofiber/fiber|go-chi/chi|jinzhu/gorm|gorm\.io|lib/pq' || true)"; \
	 if [ -n "$$bad" ]; then \
	   echo "✗ 用了 §12.4 禁掉的库："; echo "$$bad"; exit 1; fi
	@echo "✓ 铁律七：入口签名对、零 os.Getenv、零进程级 init、栈合规"

docs-check:  ## 四份文档结构检查（总纲 §4 SOP-D）
	@bash ../../../infra/scripts/docs-check.sh mdm-customer

smoke:  ## 原则一：只装这一个组件就能起来（§1.5、§3.11 第 8 条）
	@# 详细动作见 Task 17 的六项验收；这里只做最小闭环。
	@# ⚠️ brickkit 不向上找 brickkit.yaml，必须从装配仓库根目录跑——本组件
	@# 固定挂在 components/<scope>/<name>/ 下（本仓库是 components/mdm/
	@# customer），根目录固定是 ../../..（实测：计划原文这条在同一个
	@# Makefile 的其他目标都假设 CWD 是组件目录本身的前提下，会因为找不到
	@# brickkit.yaml 直接报错退出）。
	@(cd ../../.. && brickkit up --dry-run >/dev/null) && echo "✓ smoke（完整版见 make tier0）"
