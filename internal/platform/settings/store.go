package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"goforum/internal/platform/cache"
	"goforum/internal/platform/id"
	"goforum/internal/platform/secretbox"
)

type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "settings validation failed" }

type Store struct {
	db       *gorm.DB
	reg      *Registry
	mu       sync.RWMutex
	defaults map[string]json.RawMessage
	values   map[string]json.RawMessage
	version  int64
	// secretKey encrypts TypeSecret values at rest. Empty means new secret
	// writes are rejected and existing plaintext secrets stay readable.
	secretKey string
	// secretErrors records configured secrets that cannot be decrypted, e.g.
	// after APP_KEY was rotated. The admin settings page surfaces these.
	secretErrors sync.Map
	// cache lets replicas notice that another instance changed a setting.
	cache cache.Cache
	// cacheVersion is the shared generation counter observed at load time.
	cacheVersion int64
}

func NewStore(db *gorm.DB, reg *Registry) (*Store, error) {
	s := &Store{
		db:       db,
		reg:      reg,
		defaults: make(map[string]json.RawMessage),
		values:   make(map[string]json.RawMessage),
	}
	for _, d := range reg.All() {
		b, err := json.Marshal(d.Default)
		if err != nil {
			return nil, fmt.Errorf("encode default %s: %w", d.FullKey(), err)
		}
		s.defaults[d.FullKey()] = b
	}

	var rows []Setting
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		key := row.Namespace + "." + row.Key
		if _, ok := reg.Get(key); !ok {
			continue // stale setting from a removed module
		}
		s.values[key] = json.RawMessage(row.ValueJSON)
	}
	s.version = time.Now().UnixNano()
	return s, nil
}

func (s *Store) Version() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

func (s *Store) raw(key string) (json.RawMessage, bool) {
	b, ok := s.stored(key)
	if !ok {
		return nil, false
	}
	def, ok := s.reg.Get(key)
	if !ok || !def.Secret {
		return b, true
	}
	if _, configured := s.configuredRaw(key); !configured {
		return b, true // defaults are never encrypted
	}
	plain, err := s.decodeSecret(b)
	if err != nil {
		s.secretErrors.Store(key, err)
		return nil, false
	}
	s.secretErrors.Delete(key)
	return plain, true
}

// stored returns the value as persisted (plus defaults), without decrypting.
func (s *Store) stored(key string) (json.RawMessage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.values[key]; ok {
		return b, true
	}
	if b, ok := s.defaults[key]; ok {
		return b, true
	}
	return nil, false
}

func (s *Store) configuredRaw(key string) (json.RawMessage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.values[key]
	return b, ok
}

// decodeSecret unwraps an encrypted secret into the original JSON bytes.
// Legacy plaintext values are returned unchanged so an upgrade remains
// readable until SetSecretKey migrates them.
func (s *Store) decodeSecret(b json.RawMessage) (json.RawMessage, error) {
	var encoded string
	if err := json.Unmarshal(b, &encoded); err != nil {
		return b, nil
	}
	if !secretbox.IsEncrypted(encoded) {
		return b, nil
	}
	key := s.encryptionKey()
	if key == "" {
		return nil, errors.New("APP_KEY is not configured")
	}
	plain, err := secretbox.Decrypt(key, encoded)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(plain), nil
}

func (s *Store) decode(key string, dst any) bool {
	b, ok := s.raw(key)
	if !ok {
		return false
	}
	return json.Unmarshal(b, dst) == nil
}

func (s *Store) GetString(key string) string {
	var v string
	if s.decode(key, &v) {
		return v
	}
	if d, ok := s.reg.Get(key); ok {
		if v, ok := d.Default.(string); ok {
			return v
		}
	}
	return ""
}

func (s *Store) GetBool(key string) bool {
	var v bool
	if s.decode(key, &v) {
		return v
	}
	if d, ok := s.reg.Get(key); ok {
		if v, ok := d.Default.(bool); ok {
			return v
		}
	}
	return false
}

func (s *Store) GetInt(key string) int64 {
	var v int64
	if s.decode(key, &v) {
		return v
	}
	if d, ok := s.reg.Get(key); ok {
		switch x := d.Default.(type) {
		case int:
			return int64(x)
		case int64:
			return x
		}
	}
	return 0
}

func (s *Store) GetJSON(key string, dst any) bool { return s.decode(key, dst) }

func (s *Store) Def(key string) (Definition, bool) { return s.reg.Get(key) }

func (s *Store) Configured(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.values[key]
	return ok
}

// Set validates and persists a single setting, writing a revision row.
func (s *Store) Set(ctx context.Context, key string, value any, actorID string) error {
	return s.SetMany(ctx, map[string]any{key: value}, actorID)
}

// SetMany validates all values first, then persists them in one transaction.
func (s *Store) SetMany(ctx context.Context, values map[string]any, actorID string) error {
	type change struct {
		def   Definition
		value any
		json  string
		old   string
	}
	changes := make([]change, 0, len(values))
	fields := make(map[string]string)

	for key, value := range values {
		def, ok := s.reg.Get(key)
		if !ok {
			fields[key] = "未知设置项"
			continue
		}
		normalized, err := def.normalize(value)
		if err != nil {
			fields[key] = err.Error()
			continue
		}
		encoded, err := encode(normalized)
		if err != nil {
			fields[key] = err.Error()
			continue
		}
		if def.Secret {
			keyValue := s.encryptionKey()
			if keyValue == "" {
				fields[key] = "站点未配置 APP_KEY，无法保存密钥"
				continue
			}
			encrypted, err := secretbox.Encrypt(keyValue, []byte(encoded))
			if err != nil {
				fields[key] = err.Error()
				continue
			}
			quoted, err := json.Marshal(encrypted)
			if err != nil {
				fields[key] = err.Error()
				continue
			}
			encoded = string(quoted)
		}
		old := ""
		if b, ok := s.raw(key); ok {
			old = string(b)
		}
		changes = append(changes, change{def: def, value: normalized, json: encoded, old: old})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}

	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, ch := range changes {
			row := Setting{
				Namespace: ch.def.Namespace,
				Key:       ch.def.Key,
				ValueJSON: ch.json,
				UpdatedBy: actorID,
				UpdatedAt: now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "namespace"}, {Name: "key"}},
				DoUpdates: clause.Assignments(map[string]any{
					"value_json": ch.json,
					"updated_by": actorID,
					"updated_at": now,
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
			oldJSON, newJSON := ch.old, ch.json
			if ch.def.Secret || ch.def.Type == TypeSecret {
				// Never persist secret values in the revision log; it is read by
				// admins and included in backups.
				if oldJSON != "" {
					oldJSON = `"***"`
				}
				newJSON = `"***"`
			}
			rev := Revision{
				ID:        id.New(),
				Namespace: ch.def.Namespace,
				Key:       ch.def.Key,
				OldJSON:   oldJSON,
				NewJSON:   newJSON,
				ActorID:   actorID,
				CreatedAt: now,
			}
			if err := tx.Create(&rev).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, ch := range changes {
		if ch.def.Secret {
			s.secretErrors.Delete(ch.def.FullKey())
		}
	}

	if s.cache != nil {
		// Publishing a new generation lets other instances reload on their next
		// check instead of serving stale settings until restart.
		if n, err := s.cache.Incr(ctx, "settings:version", 1, 0); err == nil {
			s.cacheVersion = n
		}
	}

	s.mu.Lock()
	for _, ch := range changes {
		s.values[ch.def.FullKey()] = json.RawMessage(ch.json)
	}
	s.version = now.UnixNano()
	s.mu.Unlock()
	return nil
}

type Item struct {
	Key         string
	Label       string
	Group       string
	Type        Type
	Value       any
	Default     any
	Enum        []string
	EnumLabels  map[string]string
	Description string
	RegisterURL string
	Permission  string
	Restart     bool
	Secret      bool
	Configured  bool
	// Modified is true when the stored value differs from the default.
	Modified bool
	// DefaultText renders Default as a form-ready string (booleans, JSON and
	// numbers included) so the UI can offer a per-field reset.
	DefaultText string
}

func (s *Store) Items(group string) []Item {
	defs := s.reg.All()
	out := make([]Item, 0, len(defs))
	for _, d := range defs {
		if d.Hidden {
			continue
		}
		if group != "" && d.Group != group {
			continue
		}
		var value any
		if b, ok := s.raw(d.FullKey()); ok {
			_ = json.Unmarshal(b, &value)
		}
		if d.Secret && s.Configured(d.FullKey()) {
			value = "••••••••"
		}
		out = append(out, Item{
			Key:         d.FullKey(),
			Label:       d.Label,
			Group:       d.Group,
			Type:        d.Type,
			Value:       value,
			Default:     d.Default,
			Enum:        d.Enum,
			EnumLabels:  d.EnumLabels,
			Description: d.Description,
			RegisterURL: d.RegisterURL,
			Permission:  d.Permission,
			Restart:     d.Restart,
			Secret:      d.Secret,
			Configured:  s.Configured(d.FullKey()),
			Modified:    s.modified(d.FullKey(), value, d.Default),
		})
		out[len(out)-1].DefaultText = defaultText(out[len(out)-1])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// GroupOrder is the display order used by the admin settings page.
var GroupOrder = []string{
	"general", "registration", "verification", "content", "appearance", "navigation",
	"notification", "seo", "antispam", "media", "payment", "search",
	"security", "integrations", "maintenance", "ops",
}

func (s *Store) Groups() []string {
	seen := map[string]bool{}
	present := map[string]bool{}
	for _, d := range s.reg.All() {
		present[d.Group] = true
	}
	var groups []string
	for _, key := range GroupOrder {
		if present[key] && !seen[key] {
			seen[key] = true
			groups = append(groups, key)
		}
	}
	var extras []string
	for key := range present {
		if !seen[key] {
			extras = append(extras, key)
		}
	}
	sort.Strings(extras)
	return append(groups, extras...)
}

type NavItem struct {
	Label      string `json:"label"`
	URL        string `json:"url"`
	Icon       string `json:"icon,omitempty"`
	Permission string `json:"permission,omitempty"`
	Section    string `json:"section,omitempty"`
	External   bool   `json:"external,omitempty"`
}

type Site struct {
	Name            string
	Tagline         string
	Description     string
	URL             string
	Locale          string
	Timezone        string
	BrandColor      string
	AccentColor     string
	ColorMode       string
	ThemePreset     string
	LogoLight       string
	LogoDark        string
	FooterCopyright string
	Announcement    string
	OGImage         string
	Twitter         string
	RobotsExtra     string
	SitemapEnabled  bool
	NavHeader       []NavItem
	NavFooter       []NavItem
	GoogleVerify    string
	BingVerify      string
	BaiduVerify     string
}

func (s *Store) SiteInfo() Site {
	brand := s.GetString("site.theme.brand_color")
	accent := s.GetString("site.theme.accent_color")
	preset := s.GetString("site.theme.preset")
	var header, footer []NavItem
	_ = s.GetJSON("site.nav.header", &header)
	_ = s.GetJSON("site.nav.footer", &footer)
	return Site{
		Name:            s.GetString("site.general.name"),
		Tagline:         s.GetString("site.general.tagline"),
		Description:     s.GetString("site.general.description"),
		URL:             s.GetString("site.general.url"),
		Locale:          s.GetString("site.general.locale"),
		Timezone:        s.GetString("site.general.timezone"),
		BrandColor:      brand,
		AccentColor:     accent,
		ColorMode:       s.GetString("site.theme.color_mode"),
		ThemePreset:     preset,
		LogoLight:       s.GetString("site.theme.logo_light"),
		LogoDark:        s.GetString("site.theme.logo_dark"),
		FooterCopyright: s.GetString("site.pages.footer_copyright"),
		Announcement:    s.GetString("site.general.announcement"),
		OGImage:         s.GetString("seo.og_image"),
		Twitter:         s.GetString("seo.twitter"),
		RobotsExtra:     s.GetString("seo.robots_extra"),
		SitemapEnabled:  s.GetBool("seo.sitemap_enabled"),
		NavHeader:       header,
		NavFooter:       footer,
		GoogleVerify:    s.GetString("seo.google_verification"),
		BingVerify:      s.GetString("seo.bing_verification"),
		BaiduVerify:     s.GetString("seo.baidu_verification"),
	}
}

// ApplyThemePreset overrides brand colors for a visual preset.
func (site Site) ApplyThemePreset(preset string) Site {
	site.ThemePreset = preset
	switch preset {
	case "wechat":
		site.BrandColor, site.AccentColor = "#07c160", "#10b981"
	case "douyin":
		site.BrandColor, site.AccentColor = "#fe2c55", "#25f4ee"
	case "minimal":
		site.BrandColor, site.AccentColor = "#111111", "#6b7280"
	}
	return site
}

func (s *Store) String(key, fallback string) string {
	if s.Configured(key) {
		return s.GetString(key)
	}
	return fallback
}

// modified reports whether the effective value differs from the default.
// Secret values are never compared because they are masked when read back.
func (s *Store) modified(key string, value, def any) bool {
	if !s.Configured(key) {
		return false
	}
	if b, ok := s.raw(key); ok {
		var stored any
		if err := json.Unmarshal(b, &stored); err == nil {
			return !sameValue(stored, def)
		}
	}
	return !sameValue(value, def)
}

func sameValue(a, b any) bool {
	aj, errA := json.Marshal(a)
	bj, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(aj) == string(bj)
}

// defaultText renders a default value for form prefill.
func defaultText(it Item) string {
	switch it.Type {
	case TypeBool:
		if v, ok := it.Default.(bool); ok && v {
			return "true"
		}
		return "false"
	case TypeInt:
		if v, ok := it.Default.(int); ok {
			return fmt.Sprintf("%d", v)
		}
		if v, ok := it.Default.(int64); ok {
			return fmt.Sprintf("%d", v)
		}
		return "0"
	case TypeJSON:
		b, err := json.Marshal(it.Default)
		if err != nil {
			return ""
		}
		return string(b)
	default:
		if v, ok := it.Default.(string); ok {
			return v
		}
		return ""
	}
}

// SetSecretKey installs the key used to encrypt secret settings. Existing
// plaintext secrets are migrated in place so upgrades do not require admins to
// re-enter every SMTP/payment/OAuth credential.
func (s *Store) SetSecretKey(key string) error {
	key = strings.TrimSpace(key)
	s.mu.Lock()
	s.secretKey = key
	s.mu.Unlock()
	s.secretErrors.Range(func(k, _ any) bool {
		s.secretErrors.Delete(k)
		return true
	})
	if key == "" {
		// Surface secrets that are encrypted but cannot be read without a key.
		s.mu.RLock()
		for name, raw := range s.values {
			def, ok := s.reg.Get(name)
			if !ok || !def.Secret {
				continue
			}
			var encoded string
			if err := json.Unmarshal(raw, &encoded); err == nil && secretbox.IsEncrypted(encoded) {
				s.secretErrors.Store(name, secretbox.ErrKeyMissing)
			}
		}
		s.mu.RUnlock()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.encryptLegacySecrets(ctx)
}

func (s *Store) encryptionKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secretKey
}

// SecretErrors lists configured secrets that cannot be decrypted with the
// current key. The admin settings page displays them so a rotated APP_KEY does
// not fail silently.
func (s *Store) SecretErrors() []string {
	out := make([]string, 0)
	s.secretErrors.Range(func(k, _ any) bool {
		if key, ok := k.(string); ok {
			out = append(out, key)
		}
		return true
	})
	sort.Strings(out)
	return out
}

func (s *Store) encryptLegacySecrets(ctx context.Context) error {
	key := s.encryptionKey()
	if key == "" {
		return nil
	}
	s.mu.RLock()
	snapshot := make(map[string]json.RawMessage, len(s.values))
	for k, v := range s.values {
		snapshot[k] = v
	}
	s.mu.RUnlock()

	type update struct {
		def   Definition
		value string
	}
	updates := make([]update, 0)
	for _, def := range s.reg.All() {
		if !def.Secret {
			continue
		}
		raw, ok := snapshot[def.FullKey()]
		if !ok {
			continue
		}
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err == nil && secretbox.IsEncrypted(encoded) {
			if _, err := secretbox.Decrypt(key, encoded); err != nil {
				s.secretErrors.Store(def.FullKey(), err)
			} else {
				s.secretErrors.Delete(def.FullKey())
			}
			continue
		}
		encrypted, err := secretbox.Encrypt(key, raw)
		if err != nil {
			return err
		}
		quoted, err := json.Marshal(encrypted)
		if err != nil {
			return err
		}
		updates = append(updates, update{def: def, value: string(quoted)})
	}
	if len(updates) == 0 {
		return nil
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, u := range updates {
			if err := tx.Model(&Setting{}).
				Where("namespace = ? AND key = ?", u.def.Namespace, u.def.Key).
				Updates(map[string]any{
					"value_json": u.value,
					"updated_by": "system",
					"updated_at": now,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.mu.Lock()
	for _, u := range updates {
		s.values[u.def.FullKey()] = json.RawMessage(u.value)
	}
	s.version = now.UnixNano()
	s.mu.Unlock()
	return nil
}

// SetCache installs the shared cache and records the current generation so
// this instance can detect changes made by others.
func (s *Store) SetCache(c cache.Cache) {
	s.mu.Lock()
	s.cache = c
	s.mu.Unlock()
	if c != nil {
		if raw, ok := c.Get(context.Background(), "settings:version"); ok {
			if n, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
				s.mu.Lock()
				s.cacheVersion = n
				s.mu.Unlock()
			}
		}
	}
}

// RefreshIfStale reloads settings when another instance bumped the shared
// generation. It is cheap to call on a timer (one cache read).
func (s *Store) RefreshIfStale(ctx context.Context) (bool, error) {
	s.mu.RLock()
	c := s.cache
	seen := s.cacheVersion
	s.mu.RUnlock()
	if c == nil {
		return false, nil
	}
	raw, ok := c.Get(ctx, "settings:version")
	if !ok {
		return false, nil
	}
	current, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || current <= seen {
		return false, nil
	}
	return true, s.reload(ctx, current)
}

// reload re-reads every setting row into memory.
func (s *Store) reload(ctx context.Context, generation int64) error {
	var rows []Setting
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return err
	}
	values := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		key := row.Namespace + "." + row.Key
		if _, ok := s.reg.Get(key); !ok {
			continue
		}
		values[key] = json.RawMessage(row.ValueJSON)
	}
	s.mu.Lock()
	s.values = values
	s.cacheVersion = generation
	s.version = time.Now().UnixNano()
	s.mu.Unlock()
	return nil
}
