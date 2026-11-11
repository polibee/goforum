package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/settings"
)

func testTOTPSettings(t *testing.T, policy string) *settings.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(settings.Models()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := settings.NewRegistry()
	if err := reg.Register(settings.PlatformDefinitions()...); err != nil {
		t.Fatalf("defs: %v", err)
	}
	store, err := settings.NewStore(db, reg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if policy != "" {
		if err := store.Set(context.Background(), "site.security.admin_2fa_policy", policy, "test"); err != nil {
			t.Fatalf("set policy: %v", err)
		}
	}
	return store
}

func TestRequireLoginRedirectsAdminWithoutTOTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := testTOTPSettings(t, "superadmin")
	r := gin.New()
	r.Use(func(c *gin.Context) {
		p := &authn.Principal{ID: "u1", SuperAdmin: true, Status: "active"}
		c.Request = c.Request.WithContext(authn.WithPrincipal(c.Request.Context(), p))
		c.Next()
	})
	r.Use(RequireLogin(Deps{Settings: store, Config: &config.Config{Env: "production"}}))
	r.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/settings/security/2fa/setup" {
		t.Fatalf("gate response = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRequireLoginAllowsEnrolledOrUnrestricted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name       string
		policy     string
		principal  *authn.Principal
		wantStatus int
	}{
		{"enrolled superadmin", "superadmin", &authn.Principal{ID: "u1", SuperAdmin: true, Status: "active", TOTPEnabled: true}, http.StatusOK},
		{"normal user", "superadmin", &authn.Principal{ID: "u2", Status: "active"}, http.StatusOK},
		{"policy off", "off", &authn.Principal{ID: "u1", SuperAdmin: true, Status: "active"}, http.StatusOK},
		{"admins policy member", "admins", &authn.Principal{ID: "u3", Status: "active", Permissions: map[string]struct{}{"admin.access": {}}}, http.StatusFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testTOTPSettings(t, tc.policy)
			r := gin.New()
			p := tc.principal
			r.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(authn.WithPrincipal(c.Request.Context(), p))
				c.Next()
			})
			r.Use(RequireLogin(Deps{Settings: store, Config: &config.Config{Env: "production"}}))
			r.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestRequireLoginSkipsTOTPSetupInDevelopment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, env := range []string{"development", "dev", "", "test"} {
		t.Run(env, func(t *testing.T) {
			store := testTOTPSettings(t, "superadmin")
			r := gin.New()
			r.Use(func(c *gin.Context) {
				p := &authn.Principal{ID: "u1", SuperAdmin: true, Status: "active"}
				c.Request = c.Request.WithContext(authn.WithPrincipal(c.Request.Context(), p))
				c.Next()
			})
			r.Use(RequireLogin(Deps{Settings: store, Config: &config.Config{Env: env}}))
			r.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("dev env %q status = %d", env, rec.Code)
			}
		})
	}
}
