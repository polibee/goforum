# GoForum AI Development Guide

> For AI coding agents and new developers: understand the codebase, find the right change point, follow existing conventions, and verify the result quickly.

## 0. Fast Facts

| Item | Value |
|---|---|
| Language / framework | Go 1.27+, Gin, GORM v2 |
| Templates / frontend | Go `html/template`, Alpine.js 3 (CSP build), Tailwind, Pines |
| Entrypoint | `cmd/goforum/main.go` |
| App assembly | `Open()` in `internal/app/app.go` |
| Routing | `internal/app/router.go` + each module's `Routes()` |
| Databases | SQLite / MySQL / PostgreSQL |
| Config | defaults → YAML → `.env` → environment (`GF_*`) |
| Production migrations | `schema_migrations` ledger + `./goforum migrate` |
| Tests | `go test ./...`, `go vet ./...` |
| Frontend assets | `npm install && npm run build` (output `web/static/dist`) |
| Local preview | `GF_CONFIG=configs/goforum.preview.yaml GF_ENV_FILE=.env go run ./cmd/goforum` |
| Health | `/healthz`, `/readyz`; optional `/metrics` |

## 1. Sources of Truth and Reading Order

1. Product and capabilities: `docs/product/en-US/README.md`
2. Architecture and modules: `docs/02-模块化架构设计.md`
3. Functional spec: `docs/03-功能模块规格.md`
4. Database: `docs/04-数据库设计.md`
5. Security: `docs/11-安全与权限设计.md`
6. Deployment: `docs/13-部署与运维.md`, `docs/34-一键部署脚本与Docker.md`
7. Recent work: `docs/29`–`docs/34`
8. Code and tests win when docs disagree; update the docs or the code.

## 2. Repository Map

```text
cmd/goforum/            main, backup, restore, cleanup, migrate
internal/app/           Open, router, installer, module registry, schema/migrations
internal/modules/       feature modules (auth/community/...)
internal/platform/      platform services (settings/db/cache/mail/session/...)
web/templates/          pages, layouts, components
web/locales/en-US/      English catalogs (Chinese is the source language)
web/static/dist/        built CSS/JS
configs/                YAML examples
docs/                   design and implementation docs
scripts/                i18n wrapper, load test
```

## 3. Boot Flow

```text
main
  → config.Load(DefaultConfigPath())
  → app.Open(cfg, ...)
      → db.Open / cache.Open / storage.NewLocal
      → build module services + modules
      → NewRegistry(featureMods...)
      → dev: AutoMigrate; prod: EnsureSchema + EnsureMigrationsApplied
      → settings.NewStore + SetSecretKey (migrate legacy plaintext secrets)
      → view.New + i18n.Load
      → admin.Module + Registry complete
      → authSvc.Configure / module Configure
  → installed: Router + StartBackground
  → not installed: Installer (/install)
```

Key points:

- Production startup **never AutoMigrates**; incomplete schema or a stale migration ledger stops startup.
- The installer calls `MigrateFresh` on an empty database and records migrations.
- `development`, empty or `test` environments allow startup AutoMigrate; production does not.

## 4. Module Contract

Each module implements `internal/app.Module`:

| Method | Purpose |
|---|---|
| `Name()` | Unique name |
| `DependsOn()` | Dependencies; the registry topologically sorts modules |
| `Models()` | GORM models used by migrations/dev AutoMigrate |
| `Routes(web, api)` | Register routes |
| `Subscribe(bus)` | Subscribe to domain events |
| `Settings()` | Setting definitions |
| `Permissions()` | Permission keys |
| `Admin()` | Admin navigation contribution |

Adding a module:

1. Create `internal/modules/<name>/` with `service.go`, `module.go`, `model.go`.
2. Construct it in `featureMods` in `internal/app/app.go`.
3. Add the same module to `SchemaModels()` in `internal/app/schema.go`.
4. Add `Configure(...)` wiring in `app.Open`.
5. Add a registry topo test if needed.
6. Add a migration (section 6).

## 5. Data Layer

- Models live in each module's `model.go`; `Models()` returns pointers.
- GORM defaults to plural snake_case; implement `TableName()` when needed.
- Store timestamps in UTC; render in the site timezone.
- Production migrations live in `schemaMigrations` in `internal/app/schema.go`.
- Ledger table: `schema_migrations(version, name, applied_at)`.
- Registered: `0001 baseline_models`, `0002 legacy_subscriptions`.
- The baseline uses the current model set, so **future migrations must be idempotent** (`HasTable`/`HasColumn` first).

Add a migration:

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

Then run:

```bash
./goforum migrate
```

## 6. Auth and Security Invariants

- Passwords: Argon2id; do not write a custom hash.
- Session and email tokens are stored as hashes only.
- Permissions come from groups; multiple groups are unioned; superadmins bypass.
- Hiding UI is not a security boundary: check routes and service methods.
- Route-level limits cover login, registration, password recovery, OAuth, 2FA and email.
- Per-account daily/lifetime email quotas prevent email abuse.
- Admin endpoints are protected by the TOTP policy middleware; development bypasses enforcement and challenges.
- Secret settings use `secretbox` + `APP_KEY`; never store them in plaintext.
- Sensitive actions write audit logs.
- Uploads are image-only with MIME and size validation.
- Production requires HTTPS; cookie `Secure` follows `APP_URL`.
- Never edit applied migrations or rotate `APP_KEY` casually.

## 7. Settings

Definitions live in `internal/platform/settings/registry.go` (platform) or a module's `Settings()`.

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

- Read with `store.GetString/GetBool/GetInt/GetJSON`.
- Write with `store.Set/SetMany`; validation and revision history are automatic.
- `TypeSecret` is encrypted, UI-masked and masked in audit logs.
- The admin settings page is generated from `Group`; no page code required.
- Chinese labels/descriptions are source keys; add English to `web/locales/en-US/admin.json`.

## 8. Pages, Routes and i18n

- Register pages in `Pages` in `internal/platform/view/view.go`.
- Page files live in `web/templates/pages/*.html`; layouts in `layouts/base|auth|admin.html`.
- `view.BaseData` provides site, user, CSRF and locale.
- Translate with `{{t "中文源文本" $.Lang}}`; English catalogs live in `web/locales/en-US/<feature>.json`.
- Add a page:
  1. Add `{"my_page": {Layout: "base", File: "pages/my_page.html"}}` to `Pages`.
  2. Register a GET route in the module's `Routes`.
  3. Build `m.page(c, "Title")` and `data.Data` in the handler.
  4. Use `{{define "page"}}...{{end}}` in the template.
  5. Add English translations.
- Alpine runs the CSP build; do not add inline `<script>` or inline event handlers.

## 9. Recipes

### 9.1 Add a Setting

1. Add the definition to the relevant `Settings()` or `PlatformDefinitions()`.
2. Read it through `settings.Store`.
3. Use `TypeSecret` for credentials; encryption is automatic.
4. Add Label/Description English entries to `web/locales/en-US/admin.json`.
5. Add tests for defaults and validation.

### 9.2 Add a Permission

1. Add `{Key, Label, Group}` to the module's `Permissions()`.
2. Protect routes with `middleware.RequirePermission(md, "key")`.
3. Re-check in the service layer.
4. Add it to default/moderator groups when appropriate.
5. The admin groups page picks it up automatically.

### 9.3 Add an Email Flow

1. Reuse `EmailToken` with a purpose, or add a new purpose.
2. Validate business conditions, then `consumeEmailQuota`.
3. Create the token and send the email.
4. Keep public endpoints on a generic success message to prevent enumeration.
5. Add rate limits and tests.

### 9.4 Add a Payment Channel

1. Implement `Gateway` in `internal/modules/billing`.
2. Register the channel and initialization.
3. Add the callback path to `csrfExempt`, but verify the signature.
4. Make callbacks idempotent with order state and ledger uniqueness.
5. Document sandbox/real configuration and add tests.

## 10. Testing and Verification

```bash
gofmt -w <changed files>
go vet ./...
go test ./...
```

- Service tests use temporary SQLite (`glebarez/sqlite`).
- Settings tests need `SetSecretKey`.
- Migration tests live in `internal/platform/db/migrate_test.go`.
- Local preview:
  ```bash
  GF_CONFIG=configs/goforum.preview.yaml GF_ENV_FILE=.env go run ./cmd/goforum
  ```
- Production-mode smoke:
  ```bash
  GF_CONFIG=... GF_ENV_FILE=... ./goforum migrate
  GF_CONFIG=... GF_ENV_FILE=... ./goforum
  ```

Before finishing, verify at least: sign-in, registration/verification, posting, admin settings and the migrate command.

## 11. Do Not

- Run `AutoMigrate` in production.
- Store SMTP/payment/OAuth secrets in plaintext.
- Hide a button without a server-side permission check.
- Edit a released migration or delete `schema_migrations` rows.
- Rotate `APP_KEY` casually.
- Render user input as raw HTML.
- Claim a feature is done without tests or smoke verification.
- Change assertions instead of fixing logic.

## 12. Suggested AI Workflow

1. **Search before editing**: use `rg` to find similar implementations.
2. **Small diffs**: prefer local Edits; no unrelated refactors.
3. **Read first**: for settings/migrations/auth, read the docs and tests first.
4. **Test-driven**: add a failing case, implement, then run the full suite.
5. **Stay bilingual**: add English catalog entries with every Chinese string.
6. **Preserve invariants**: service checks, encrypted secrets, audit, rate limits.
7. **Update docs**: sync `docs/` and product docs when behavior changes.
8. **Report honestly**: state failing tests, unverified items and remaining risks.

## 13. Key Files

| Goal | File |
|---|---|
| Boot/assembly | `cmd/goforum/main.go`, `internal/app/app.go` |
| Routing/middleware | `internal/app/router.go`, `internal/platform/middleware/` |
| Module registry | `internal/app/registry.go` |
| Schema/migrations | `internal/app/schema.go`, `internal/platform/db/migrate.go` |
| Settings | `internal/platform/settings/` |
| Auth | `internal/modules/auth/` |
| Content | `internal/modules/community/` |
| Moderation | `internal/modules/moderation/` |
| Billing | `internal/modules/billing/` |
| Templates | `web/templates/` |
| Translations | `web/locales/en-US/` |
| Product docs | `docs/product/zh-CN/`, `docs/product/en-US/` |
