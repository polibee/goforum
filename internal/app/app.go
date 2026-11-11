package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"goforum/internal/modules/admin"
	"goforum/internal/modules/ads"
	"goforum/internal/modules/auth"
	"goforum/internal/modules/billing"
	"goforum/internal/modules/community"
	"goforum/internal/modules/engagement"
	"goforum/internal/modules/gamification"
	"goforum/internal/modules/media"
	"goforum/internal/modules/messaging"
	"goforum/internal/modules/moderation"
	"goforum/internal/modules/notification"
	"goforum/internal/modules/placeholder"
	seomod "goforum/internal/modules/seo"
	sitemod "goforum/internal/modules/site"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/cache"
	"goforum/internal/platform/config"
	"goforum/internal/platform/db"
	"goforum/internal/platform/events"
	"goforum/internal/platform/i18n"
	"goforum/internal/platform/mail"
	"goforum/internal/platform/markdown"
	"goforum/internal/platform/metrics"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/searchx"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/storage"
	"goforum/internal/platform/view"
	webfs "goforum/web"
)

type App struct {
	Cfg        *config.Config
	ConfigPath string
	Version    string
	Log        *slog.Logger

	DB            *gorm.DB
	Settings      *settings.Store
	Sessions      *session.Store
	Auth          *auth.Service
	Community     *community.Service
	Engagement    *engagement.Service
	Messaging     *messaging.Service
	Media         *media.Service
	Notifications *notification.Service
	Moderation    *moderation.Service
	Gamification  *gamification.Service
	Billing       *billing.Service
	Storage       storage.Storage
	Search        *searchx.DBEngine
	Audit         *audit.Service
	Perms         *permission.Registry
	Registry      *Registry
	Renderer      *view.Renderer
	Bus           events.Bus
	LocalBus      *events.MemoryBus
	RedisBus      *events.RediBus
	Cache         cache.Cache
	Metrics       *metrics.Registry
	I18n          *i18n.Bundle

	adminModule  *admin.Module
	notifyModule *notification.Module
}

func Open(cfg *config.Config, configPath, version string, log *slog.Logger) (app *App, err error) {
	startedAt := time.Now()
	gdb, err := db.Open(cfg.Database)
	if err != nil {
		return nil, err
	}

	var cacheStore cache.Cache
	// Release opened infrastructure when any later step fails, so a failed
	// startup does not keep database/file handles locked (notably on Windows).
	defer func() {
		if err != nil {
			if cacheStore != nil {
				_ = cacheStore.Close()
			}
			_ = db.Close(gdb)
		}
	}()

	stg, err := storage.NewLocal(cfg.Storage.LocalPath, cfg.Storage.BaseURL)
	if err != nil {
		return nil, err
	}

	// The cache is shared infrastructure: rate limits, counters and settings
	// invalidation all go through it, so open it before wiring services.
	cacheStore, err = cache.Open(context.Background(),
		cfg.Cache.Driver, cfg.Cache.RedisURL, cfg.Cache.Prefix, cfg.Cache.TTL.Std())
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	middleware.SetRateLimitCache(cacheStore)

	// A Redis client is shared by the event bus and the SSE hub so both can
	// reach sibling instances. Without it everything stays process-local.
	redisClient := cacheStore.RedisClient()
	localBus := events.NewMemoryBus(log)
	var bus events.Bus = localBus
	var redisBus *events.RediBus
	if redisClient != nil {
		rb, err := events.NewRedisBus(context.Background(), localBus, redisClient, log)
		if err != nil {
			log.Warn("redis event bus disabled; events stay local", "error", err)
		} else {
			bus = rb
			redisBus = rb
		}
	}
	md := markdown.New()
	mailer := mail.Mailer(mail.NewLogMailer(log))

	authSvc := auth.New(gdb)
	authMod := auth.NewModule(authSvc)

	communitySvc := community.NewService(gdb)
	communityMod := community.New(communitySvc)

	mediaSvc := media.NewService(gdb)
	mediaMod := media.New(gdb, mediaSvc)

	notificationSvc := notification.NewService(gdb)
	notificationMod := notification.New(gdb, notificationSvc)

	engagementSvc := engagement.NewService(gdb)
	engagementMod := engagement.New(gdb, engagementSvc)

	messagingSvc := messaging.NewService(gdb)
	messagingMod := messaging.New(gdb, messagingSvc)

	moderationSvc := moderation.NewService(gdb)
	moderationMod := moderation.New(gdb, moderationSvc)

	gamificationSvc := gamification.NewService(gdb)
	gamificationMod := gamification.New(gdb, gamificationSvc)

	billingSvc := billing.NewService(gdb)
	billingMod := billing.New(gdb, billingSvc)

	seoMod := seomod.New(gdb)
	siteMod := sitemod.New(gdb)
	adsSvc := ads.NewService(gdb)
	adsMod := ads.New(adsSvc)

	auditSvc := audit.New(gdb)

	featureMods := []Module{
		authMod,
		communityMod,
		mediaMod,
		engagementMod,
		messagingMod,
		notificationMod,
		moderationMod,
		gamificationMod,
		billingMod,
		seoMod,
		siteMod,
		adsMod,
		placeholder.New("search", []string{"auth", "community"}, nil,
			[]settings.Definition{
				{Namespace: "search", Key: "min_query_length", Group: "search", Label: "最短查询长度", Type: settings.TypeInt, Default: 2, Min: 1, Max: 20, Permission: "settings.write"},
			}, nil),
	}

	tmpRegistry, err := NewRegistry(featureMods...)
	if err != nil {
		return nil, err
	}

	platformModels := append(settings.Models(), session.Models()...)
	platformModels = append(platformModels, audit.Models()...)
	models := append(platformModels, tmpRegistry.Models()...)
	// AutoMigrate rewrites tables and cannot be rolled back, so it only runs in
	// development. Production deployments apply the schema during setup or with
	// `goforum migrate`, and startup verifies the migration ledger.
	if cfg.IsDevelopment() || cfg.Env == "" || cfg.Env == "test" {
		if err := db.Migrate(gdb, models...); err != nil {
			return nil, err
		}
		if err := migrateLegacySubscriptions(gdb, log); err != nil {
			return nil, err
		}
	} else {
		if err := db.EnsureSchema(gdb, models...); err != nil {
			// A fresh empty database is expected to fail here: the installer will
			// create the schema. Once users exist, an incomplete schema is a broken
			// upgrade and must stop startup instead of serving failing requests.
			if hasUsers, checkErr := databaseHasUsers(gdb); checkErr == nil && !hasUsers {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %v", ErrSchemaIncomplete, err)
		}
		if err := db.EnsureMigrationsApplied(gdb, schemaMigrations(log)); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSchemaIncomplete, err)
		}
	}

	searchEngine := searchx.NewDBEngine(gdb)
	if err := searchEngine.EnsureSchema(context.Background()); err != nil {
		log.Warn("search schema init failed", "error", err)
	}

	settingsReg := settings.NewRegistry()
	if err := settingsReg.Register(settings.PlatformDefinitions()...); err != nil {
		return nil, err
	}
	if err := settingsReg.Register(tmpRegistry.Settings()...); err != nil {
		return nil, err
	}
	settingsStore, err := settings.NewStore(gdb, settingsReg)
	if err != nil {
		return nil, err
	}
	if err := settingsStore.SetSecretKey(cfg.Security.SecretKey); err != nil {
		log.Warn("settings secret migration failed; secrets stay readable but may remain plaintext", "error", err)
	}
	settingsStore.SetCache(cacheStore)
	mailer = mail.NewDynamic(log, cfg, settingsStore)

	templateFS, err := fs.Sub(webfs.FS, "templates")
	if err != nil {
		return nil, err
	}
	renderer, err := view.New(templateFS, cfg.IsDevelopment(), log)
	if err != nil {
		return nil, err
	}
	localeFS, err := fs.Sub(webfs.FS, "locales")
	if err != nil {
		return nil, err
	}
	bundle, err := i18n.Load(localeFS)
	if err != nil {
		return nil, err
	}
	renderer.SetTranslator(bundle.Translate)
	renderer.SetAssetVersion(view.AssetVersion(webfs.FS))
	view.SetGlobalTranslator(bundle.Translate)

	sessions := session.NewStore(gdb, cfg.Security.SessionTTL.Std())

	perms := permission.NewRegistry()
	if err := perms.Register(tmpRegistry.Permissions()...); err != nil {
		return nil, err
	}
	if err := perms.Register(admin.Permissions()...); err != nil {
		return nil, err
	}

	adminMod := admin.New(admin.Deps{
		Config:    cfg,
		DB:        gdb,
		Settings:  settingsStore,
		Auth:      authSvc,
		Sessions:  sessions,
		Perms:     perms,
		Renderer:  renderer,
		Audit:     auditSvc,
		I18n:      bundle,
		Version:   version,
		StartedAt: startedAt,
		AdsCache:  adsSvc,
		Log:       log,
	})

	allMods := append(featureMods, adminMod)
	registry, err := NewRegistry(allMods...)
	if err != nil {
		return nil, err
	}
	adminMod.SetRegistry(registry)
	authMod.SetRegistry(registry)

	authMod.Configure(renderer, sessions, settingsStore, cfg, version)
	authMod.SetAudit(auditSvc)
	authSvc.Configure(settingsStore, mailer, cfg.HTTP.BaseURL)
	authSvc.SetSecretKey(cfg.Security.SecretKey)
	authSvc.SetEnv(cfg.Env)

	communitySvc.Configure(settingsStore, bus, md, authSvc)
	communitySvc.SetCache(cacheStore)
	communitySvc.SetSearch(searchEngine)
	authSvc.SetActivity(communitySvc)
	if err := authSvc.EnsureDefaultGroup(context.Background()); err != nil {
		log.Warn("ensure default group failed", "error", err)
	}
	if err := authSvc.EnsureModeratorGroup(context.Background()); err != nil {
		log.Warn("ensure moderator group failed", "error", err)
	}
	communityMod.Configure(renderer, settingsStore, authSvc, sessions, cfg, log, version)
	communityMod.SetRegistry(registry)
	communityMod.SetAudit(auditSvc)

	gamificationMod.Configure(settingsStore, communitySvc, auditSvc, bus, renderer, authSvc, sessions, cfg, log, version)
	communityMod.SetTips(gamificationSvc)
	communityMod.SetBadges(gamificationSvc)
	gamificationMod.SetRegistry(registry)
	authSvc.SetInvites(gamificationSvc)
	if err := gamificationSvc.EnsureDefaults(context.Background()); err != nil {
		log.Warn("ensure gamification defaults failed", "error", err)
	}

	moderationMod.Configure(settingsStore, communitySvc, communitySvc, messagingSvc, auditSvc, bus,
		renderer, authSvc, sessions, cfg, log, version)
	moderationMod.SetRegistry(registry)
	communitySvc.SetGuard(moderationSvc)
	communitySvc.SetQueue(moderationSvc)

	billingMod.Configure(settingsStore, gamificationSvc, auditSvc, bus, renderer, authSvc, sessions, cfg, log, version)
	billingMod.SetRegistry(registry)
	if err := billingSvc.EnsureDefaults(context.Background()); err != nil {
		log.Warn("ensure billing defaults failed", "error", err)
	}

	engagementMod.Configure(communitySvc, bus, renderer, settingsStore, authSvc, sessions, cfg, log, version)
	communityMod.SetEngagement(engagementSvc)

	mediaMod.Configure(settingsStore, stg, authSvc, sessions, cfg, log, renderer, version)
	seoMod.Configure(settingsStore, renderer, authSvc, sessions, cfg, log, version)
	seoMod.SetRegistry(registry)
	siteMod.Configure(settingsStore, renderer, authSvc, sessions, cfg, log, version)
	siteMod.SetRegistry(registry)
	adsMod.Configure(settingsStore, renderer, authSvc, sessions, cfg, log, version)
	adsMod.SetBilling(billingSvc)
	adsMod.SetRegistry(registry)
	if err := adsSvc.EnsureDefaults(context.Background()); err != nil {
		log.Warn("ensure ad slots failed", "error", err)
	}
	view.SetLayoutProvider(siteMod.Layout)
	view.SetAdProvider(adsSvc.Slots)
	if err := siteMod.EnsureDefaults(context.Background()); err != nil {
		log.Warn("ensure site pages failed", "error", err)
	}
	messagingMod.Configure(md, bus, renderer, settingsStore, authSvc, sessions, cfg, log, version)
	notificationMod.Configure(renderer, settingsStore, authSvc, sessions, cfg, log, version)
	if redisClient != nil {
		notificationMod.SetSSERedis(redisClient)
	}

	if err := communitySvc.EnsureDefaults(context.Background()); err != nil {
		log.Warn("ensure default category failed", "error", err)
	}
	if err := communitySvc.EnsureNumbers(context.Background()); err != nil {
		log.Warn("backfill topic numbers failed", "error", err)
	}

	if err := registry.Subscribe(bus); err != nil {
		return nil, err
	}

	return &App{
		Cfg:           cfg,
		ConfigPath:    configPath,
		Version:       version,
		Log:           log,
		DB:            gdb,
		Settings:      settingsStore,
		Sessions:      sessions,
		Auth:          authSvc,
		Community:     communitySvc,
		Engagement:    engagementSvc,
		Messaging:     messagingSvc,
		Media:         mediaSvc,
		Notifications: notificationSvc,
		Moderation:    moderationSvc,
		Gamification:  gamificationSvc,
		Billing:       billingSvc,
		Storage:       stg,
		Search:        searchEngine,
		Audit:         auditSvc,
		Perms:         perms,
		Registry:      registry,
		Renderer:      renderer,
		Bus:           bus,
		LocalBus:      localBus,
		RedisBus:      redisBus,
		Cache:         cacheStore,
		Metrics:       metrics.New(),
		I18n:          bundle,
		adminModule:   adminMod,
		notifyModule:  notificationMod,
	}, nil
}
