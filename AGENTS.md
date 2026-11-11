# AGENTS

GoForum is a self-hosted modular forum built with Go + Gin + GORM + Go templates + Alpine.js (CSP) + Tailwind.

Read one of these before making non-trivial changes:

- English: `docs/product/en-US/AI-DEVELOPMENT.md`
- 中文: `docs/product/zh-CN/AI-DEVELOPMENT.md`
- Product overview: `docs/product/en-US/README.md` / `docs/product/zh-CN/README.md`

## Non-negotiables

- Production startup never runs `AutoMigrate`; use `./goforum migrate`.
- Add new migrations at the end of `schemaMigrations` in `internal/app/schema.go`; never edit an applied migration; keep migrations idempotent.
- Secret settings are AES-256-GCM encrypted through `APP_KEY`; never store credentials in plaintext.
- Server-side permission checks are mandatory; hiding UI is not authorization.
- Preserve audit logs, rate limits, email quotas and CSRF/security headers.
- Keep zh-CN source strings and `web/locales/en-US/*.json` in sync.
- Do not include user HTML without sanitization.

## Commands

```bash
gofmt -w <changed files>
go vet ./...
go test ./...

npm install && npm run build   # frontend assets
make build                      # bin/goforum

GF_CONFIG=configs/goforum.preview.yaml GF_ENV_FILE=.env go run ./cmd/goforum
./goforum migrate
./goforum backup
```

## Where to change

| Goal | File |
|---|---|
| Boot / module wiring | `internal/app/app.go` |
| Routes / middleware | `internal/app/router.go`, `internal/platform/middleware/` |
| Module contract | `internal/app/registry.go` |
| Schema / migrations | `internal/app/schema.go`, `internal/platform/db/migrate.go` |
| Settings | `internal/platform/settings/` |
| Auth / 2FA | `internal/modules/auth/` |
| Templates | `web/templates/` |
| English copy | `web/locales/en-US/` |
| Deployment | `scripts/deploy.sh`, `scripts/docker-deploy.sh`, `Dockerfile`, `docker-compose.yml` |

## Workflow

1. Search for an existing pattern with `rg` before adding a new one.
2. Make the smallest coherent change; avoid unrelated refactors.
3. Add or update tests, then run the full suite.
4. Update docs when behavior, settings or endpoints change.
5. Report failures and unverified items explicitly; never claim a feature is done without verification.
