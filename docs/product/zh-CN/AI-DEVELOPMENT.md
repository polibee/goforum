# GoForum AI 快速开发文档

> 面向 AI 编码代理与新加入的开发者：用最短路径理解代码库、找到修改点、按既有约定实现并验证。

## 0. 快速事实

| 项 | 值 |
|---|---|
| 语言 / 框架 | Go 1.27+、Gin、GORM v2 |
| 模板 / 前端 | Go `html/template`、Alpine.js 3（CSP 构建）、Tailwind、Pines |
| 入口 | `cmd/goforum/main.go` |
| 应用装配 | `internal/app/app.go` 的 `Open()` |
| 路由装配 | `internal/app/router.go` + 各模块 `Routes()` |
| 数据库 | SQLite / MySQL / PostgreSQL |
| 配置 | 默认值 → YAML → `.env` → 环境变量（`GF_*`） |
| 生产迁移 | `schema_migrations` 台账 + `./goforum migrate` |
| 测试 | `go test ./...`、`go vet ./...` |
| 前端资源 | `npm install && npm run build`（产物 `web/static/dist`） |
| 本地预览 | `GF_CONFIG=configs/goforum.preview.yaml GF_ENV_FILE=.env go run ./cmd/goforum` |
| 健康检查 | `/healthz`、`/readyz`；可选 `/metrics` |

## 1. 事实来源与阅读顺序

1. 产品与能力：`docs/product/zh-CN/README.md`
2. 架构与模块：`docs/02-模块化架构设计.md`
3. 功能规格：`docs/03-功能模块规格.md`
4. 数据库：`docs/04-数据库设计.md`
5. 安全：`docs/11-安全与权限设计.md`
6. 部署：`docs/13-部署与运维.md`、`docs/34-一键部署脚本与Docker.md`
7. 近期实现：`docs/29`–`docs/34`
8. 最终以代码与测试为准；文档冲突时优先修代码或同步文档。

## 2. 仓库地图

```text
cmd/goforum/            main、backup、restore、cleanup、migrate
internal/app/           Open、路由、安装向导、模块注册、schema/migrations
internal/modules/       业务模块（auth/community/...）
internal/platform/      平台能力（settings/db/cache/mail/session/...）
web/templates/          页面、布局、组件
web/locales/en-US/      英文翻译（中文是源语言，不需要文件）
web/static/dist/        构建后的 CSS/JS
configs/                YAML 配置示例
docs/                   设计与实现文档
scripts/                i18n 包装、压测
```

## 3. 启动链路

```text
main
  → config.Load(DefaultConfigPath())
  → app.Open(cfg, ...)
      → db.Open / cache.Open / storage.NewLocal
      → 构造各模块 Service + Module
      → NewRegistry(featureMods...)
      → 开发模式 AutoMigrate；生产模式 EnsureSchema + EnsureMigrationsApplied
      → settings.NewStore + SetSecretKey（迁移旧明文密钥）
      → view.New + i18n.Load
      → admin.Module + Registry 完成
      → authSvc.Configure / 各模块 Configure
  → 已安装：Router + StartBackground
  → 未安装：Installer（/install）
```

关键点：

- 生产启动**不会 AutoMigrate**；schema 或迁移台账不完整会拒绝启动。
- 安装向导在空库上调用 `MigrateFresh` 建表并记录迁移。
- `env: development`、空环境或 `test` 才允许启动时 AutoMigrate；生产环境不会。

## 4. 模块契约

每个模块实现 `internal/app.Module`：

| 方法 | 用途 |
|---|---|
| `Name()` | 唯一名称 |
| `DependsOn()` | 依赖模块名，注册表做拓扑排序 |
| `Models()` | GORM 模型，供迁移/开发建表 |
| `Routes(web, api)` | 注册路由 |
| `Subscribe(bus)` | 订阅领域事件 |
| `Settings()` | 设置定义 |
| `Permissions()` | 权限键 |
| `Admin()` | 后台导航贡献 |

新增模块：

1. 在 `internal/modules/<name>/` 建 `service.go`、`module.go`、`model.go`。
2. 在 `internal/app/app.go` 构造并加入 `featureMods`。
3. 在 `internal/app/schema.go` 的 `SchemaModels()` 中加入同一模块（保持清单一致）。
4. 在 `internal/app/app.go` 写 `Configure(...)` 注入依赖。
5. 如需要，加入 `internal/app/registry_test.go` 的拓扑测试。
6. 加迁移（见第 6 节）。

## 5. 数据层

- 模型放在各模块 `model.go`，`Models()` 返回指针列表。
- GORM 表名默认复数蛇形；需要时实现 `TableName()`。
- 时间统一 UTC；业务展示按站点时区。
- 生产迁移：`internal/app/schema.go` 的 `schemaMigrations`。
- 迁移台账表：`schema_migrations(version, name, applied_at)`。
- 已注册：`0001 baseline_models`、`0002 legacy_subscriptions`。
- 基线使用当前模型集合，因此**后续迁移必须幂等**（先 `HasTable`/`HasColumn`）。

新增迁移：

```go
{
    Version: 3,
    Name:    "add_example_column",
    Up: func(_ context.Context, tx *gorm.DB) error {
        if tx.Migrator().HasColumn("topics", "example") {
            return nil
        }
        return tx.Migrator().AddColumn(&community.Topic{}, "Example")
    },
},
```

然后执行：

```bash
./goforum migrate
```

## 6. 认证与安全不变量

- 密码：Argon2id；不要自建哈希。
- 会话令牌、邮箱令牌只存哈希。
- 权限：用户组显式授予；多组并集；超管绕过。
- 前端隐藏不是安全边界：路由和服务层都要校验。
- 登录、注册、找回密码、OAuth、2FA、邮件接口有路由限流。
- 邮件有账号级单日/累计配额。
- 管理级接口受 TOTP 策略中间件保护；开发环境跳过 TOTP 强制与挑战。
- 设置中的 Secret 使用 `secretbox` + `APP_KEY` 加密；不要明文落库。
- 所有敏感操作写审计日志。
- 上传只允许图片并校验 MIME/大小。
- 生产必须 HTTPS；Cookie 的 Secure 依据 `APP_URL`。
- 不要修改已应用的迁移；不要随意更换 `APP_KEY`。

## 7. 设置系统

设置定义在 `internal/platform/settings/registry.go`（平台）或各模块 `Settings()`。

```go
settings.Definition{
    Namespace: "site",
    Key:       "feature.enabled",
    Group:     "content",
    Label:     "启用功能",
    Type:      settings.TypeBool,
    Default:   true,
    Permission: "settings.write",
    Description: "说明文字",
}
```

- 读取：`store.GetString/GetBool/GetInt/GetJSON`。
- 写入：`store.Set/SetMany`，自动校验、写版本历史。
- `TypeSecret` 自动加密、UI 掩码、审计掩码。
- 后台设置页按 `Group` 自动生成，无需写页面。
- 标签/说明用中文源文本，英文在 `web/locales/en-US/admin.json` 补翻译。

## 8. 页面、路由与 i18n

- 页面注册：`internal/platform/view/view.go` 的 `Pages`。
- 页面文件：`web/templates/pages/*.html`，布局 `layouts/base|auth|admin.html`。
- 页面数据：`view.BaseData` 提供站点、用户、CSRF、语言等。
- 翻译：`{{t "中文源文本" $.Lang}}`；英文文件在 `web/locales/en-US/<feature>.json`。
- 新增页面：
  1. `Pages` 增加 `{"my_page": {Layout: "base", File: "pages/my_page.html"}}`
  2. 模块 `Routes` 注册 GET。
  3. Handler 组装 `m.page(c, "标题")` 与 `data.Data`。
  4. 模板 `{{define "page"}}...{{end}}`。
  5. 补 `en-US` 翻译。
- Alpine 使用 CSP 构建；不要写内联 `<script>` 或内联事件处理器。

## 9. 常见任务配方

### 9.1 新增设置

1. 在对应 `Settings()` 或 `PlatformDefinitions()` 加定义。
2. 业务代码通过 `settings.Store` 读取。
3. 如果是 Secret，确认 `TypeSecret`；无需手动加密。
4. 在 `web/locales/en-US/admin.json` 加 Label/Description 翻译。
5. 加测试覆盖默认值和保存校验。

### 9.2 新增权限

1. 模块 `Permissions()` 加 `{Key, Label, Group}`。
2. 路由加 `middleware.RequirePermission(md, "key")`。
3. 服务层再校验一次（防止绕过路由）。
4. 默认组/版主组按需补权限。
5. 后台用户组页面自动出现。

### 9.3 新增邮件流程

1. 复用 `EmailToken`（用途字符串）或新增用途。
2. 先校验业务条件，再 `consumeEmailQuota`。
3. 创建令牌并发送邮件。
4. 公开入口保持统一成功文案，防账号枚举。
5. 加限流与测试。

### 9.4 新增支付渠道

1. 在 `internal/modules/billing` 实现 `Gateway`。
2. 注册到渠道表/初始化。
3. 回调地址加入 `csrfExempt`，但必须做签名校验。
4. 回调幂等：订单状态 + 账本唯一约束。
5. 补沙箱/真实配置说明与测试。

## 10. 测试与验证

```bash
gofmt -w <changed files>
go vet ./...
go test ./...
```

- 服务测试使用临时 SQLite（`glebarez/sqlite`）。
- 设置相关测试需要 `SetSecretKey`。
- 迁移测试在 `internal/platform/db/migrate_test.go`。
- 本地预览：
  ```bash
  GF_CONFIG=configs/goforum.preview.yaml GF_ENV_FILE=.env go run ./cmd/goforum
  ```
- 生产模式冒烟：
  ```bash
  GF_CONFIG=... GF_ENV_FILE=... ./goforum migrate
  GF_CONFIG=... GF_ENV_FILE=... ./goforum
  ```

提交前至少验证：登录、注册/验证、发帖、后台设置、迁移命令。

## 11. 禁止事项

- 生产启动执行 `AutoMigrate`。
- 明文保存 SMTP/支付/OAuth 密钥。
- 只在前端隐藏按钮而不做服务层权限校验。
- 修改已发布的迁移或删除 `schema_migrations` 行。
- 随意更换 `APP_KEY`。
- 把用户输入直接输出为 HTML。
- 在没有测试/冒烟的情况下声称功能完成。
- 为了通过测试而修改测试断言而不是修复逻辑。

## 12. AI 工作流建议

1. **先搜后改**：用 `rg` 找同类实现，复用现有模式。
2. **小步修改**：优先局部 Edit，不做无关重构。
3. **先读后写**：涉及设置/迁移/认证时先读对应文档与测试。
4. **测试驱动**：先加失败用例，再实现，再跑全量。
5. **保持双语**：新增中文文案时同步 `en-US`。
6. **保持安全不变量**：服务层校验、密钥加密、审计、限流。
7. **更新文档**：行为变化同步 `docs/` 与产品文档。
8. **诚实报告**：测试失败、未验证项、剩余风险要明确写出。

## 13. 关键文件索引

| 目标 | 文件 |
|---|---|
| 启动/装配 | `cmd/goforum/main.go`、`internal/app/app.go` |
| 路由/中间件 | `internal/app/router.go`、`internal/platform/middleware/` |
| 模块注册 | `internal/app/registry.go` |
| Schema/迁移 | `internal/app/schema.go`、`internal/platform/db/migrate.go` |
| 设置 | `internal/platform/settings/` |
| 认证 | `internal/modules/auth/` |
| 内容 | `internal/modules/community/` |
| 治理 | `internal/modules/moderation/` |
| 计费 | `internal/modules/billing/` |
| 模板 | `web/templates/` |
| 翻译 | `web/locales/en-US/` |
| 产品文档 | `docs/product/zh-CN/`、`docs/product/en-US/` |
