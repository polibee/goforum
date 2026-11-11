package ads

import (
	"context"
	"encoding/json"
	"html/template"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"goforum/internal/modules/billing"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

type Service struct {
	db       *gorm.DB
	settings *settings.Store
	billing  *billing.Service
	mu       sync.Mutex
	cache    []Ad
	cacheAt  time.Time
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(st *settings.Store, billingSvc *billing.Service) {
	s.settings = st
	s.billing = billingSvc
}

type Placement struct {
	Key         string
	Label       string
	Description string
	Size        string
	Height      int
}

var Placements = []Placement{
	{Key: "header", Label: "页头横幅", Description: "显示在所有前台页面顶部，适合品牌曝光。", Size: "970 × 90", Height: 90},
	{Key: "home_top", Label: "首页顶部", Description: "首页主题列表上方，曝光强、干扰低。", Size: "970 × 110", Height: 110},
	{Key: "sidebar", Label: "侧边栏", Description: "首页与主题页右侧，适合图片或文字卡片。", Size: "300 × 250", Height: 250},
	{Key: "topic_inline", Label: "主题内嵌", Description: "主题回复区域上方，适合精准内容推广。", Size: "728 × 120", Height: 120},
	{Key: "footer", Label: "页脚横幅", Description: "所有前台页面页脚上方。", Size: "970 × 90", Height: 90},
}

func PlacementByKey(key string) (Placement, bool) {
	for _, p := range Placements {
		if p.Key == key {
			return p, true
		}
	}
	return Placement{}, false
}

var slotDefaults = []AdSlot{
	{Key: "header", Name: "页头横幅", Description: "显示在所有前台页面顶部，适合品牌曝光。", Size: "970 × 90", Height: 90, Sort: 10, Enabled: true, SellEnabled: true, MaxText: 1, MaxImage: 1, PriceText: 4900, PriceImage: 7900},
	{Key: "home_top", Name: "首页顶部", Description: "首页主题列表上方，曝光强、干扰低。", Size: "970 × 110", Height: 110, Sort: 20, Enabled: true, SellEnabled: true, MaxText: 1, MaxImage: 1, PriceText: 2900, PriceImage: 4900},
	{Key: "sidebar", Name: "侧边栏", Description: "首页右侧卡片，可放多个文字或图片广告。", Size: "300 × 250", Height: 250, Sort: 30, Enabled: true, SellEnabled: true, MaxText: 2, MaxImage: 1, PriceText: 1900, PriceImage: 3900},
	{Key: "topic_inline", Name: "主题内嵌", Description: "主题回复区域上方，适合精准内容推广。", Size: "728 × 120", Height: 120, Sort: 40, Enabled: true, SellEnabled: true, MaxText: 1, MaxImage: 1, PriceText: 1500, PriceImage: 2900},
	{Key: "footer", Name: "页脚横幅", Description: "所有前台页面页脚上方。", Size: "970 × 90", Height: 90, Sort: 50, Enabled: true, SellEnabled: true, MaxText: 1, MaxImage: 1, PriceText: 990, PriceImage: 1900},
}

func (s *Service) EnsureDefaults(ctx context.Context) error {
	for _, def := range slotDefaults {
		var n int64
		if err := s.db.WithContext(ctx).Unscoped().Model(&AdSlot{}).Where("key = ?", def.Key).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if s.settings != nil {
			if v := s.settings.GetInt("ads.price." + def.Key + ".text"); v > 0 {
				def.PriceText = v
			}
			if v := s.settings.GetInt("ads.price." + def.Key + ".image"); v > 0 {
				def.PriceImage = v
			}
		}
		if err := s.db.WithContext(ctx).Create(&def).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SlotConfigs(ctx context.Context, onlyEnabled bool) ([]AdSlot, error) {
	q := s.db.WithContext(ctx).Model(&AdSlot{})
	if onlyEnabled {
		q = q.Where("enabled = ?", true)
	}
	var rows []AdSlot
	err := q.Order("sort ASC, created_at ASC").Find(&rows).Error
	return rows, err
}

func (s *Service) SlotByKey(ctx context.Context, key string) (*AdSlot, error) {
	var row AdSlot
	if err := s.db.WithContext(ctx).First(&row, "key = ?", key).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Service) UpdateSlot(ctx context.Context, id string, updates map[string]any) error {
	if err := s.db.WithContext(ctx).Model(&AdSlot{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	s.invalidate()
	return nil
}

func (s *Service) DeleteSlot(ctx context.Context, id string) error {
	var slot AdSlot
	if err := s.db.WithContext(ctx).First(&slot, "id = ?", id).Error; err != nil {
		return err
	}
	var ads int64
	if err := s.db.WithContext(ctx).Model(&Ad{}).Where("slot = ?", slot.Key).Count(&ads).Error; err != nil {
		return err
	}
	if ads > 0 {
		return httpx.Invalid(map[string]string{"slot": "该广告位已有广告，请先删除广告"})
	}
	if err := s.db.WithContext(ctx).Delete(&AdSlot{}, "id = ?", id).Error; err != nil {
		return err
	}
	s.invalidate()
	return nil
}

type Channel struct {
	ID     string
	Name   string
	Driver string
}

func (s *Service) Channels(ctx context.Context) []Channel {
	if s.billing == nil {
		return nil
	}
	rows, _ := s.billing.ListChannels(ctx, true)
	out := make([]Channel, 0, len(rows))
	for _, row := range rows {
		if row.Driver == "balance" {
			continue
		}
		out = append(out, Channel{ID: row.ID, Name: row.Name, Driver: row.Driver})
	}
	return out
}

func (s *Service) Subscribe(bus events.Bus) error {
	return bus.Subscribe("order.paid", s.onOrderPaid)
}

type orderPaidPayload struct {
	ProductType string `json:"product_type"`
	ProductID   string `json:"product_id"`
	ProductSlug string `json:"product_slug"`
}

func (s *Service) onOrderPaid(ctx context.Context, e events.Event) error {
	var p orderPaidPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.ProductType != "ad_slot" || p.ProductID == "" {
		return nil
	}
	return s.Fulfill(ctx, p.ProductID)
}

// Fulfill activates a paid ad, optionally holding it for review.
func (s *Service) Fulfill(ctx context.Context, adID string) error {
	status := "active"
	if s.settings != nil && s.settings.GetBool("ads.self_serve.review") {
		status = "pending_review"
	}
	return s.SetStatus(ctx, adID, status, "")
}

func (s *Service) MaxMonths() int {
	if s.settings == nil {
		return 12
	}
	n := int(s.settings.GetInt("ads.self_serve.max_months"))
	if n < 1 {
		n = 1
	}
	if n > 36 {
		n = 36
	}
	return n
}

func (s *Service) SelfServeEnabled() bool {
	return s.settings != nil && s.settings.GetBool("ads.self_serve.enabled")
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.cache = nil
	s.cacheAt = time.Time{}
	s.mu.Unlock()
}

// Slots resolves the active ads for each enabled placement. Each placement
// can show up to MaxText text ads, MaxImage image ads and one HTML ad.
func (s *Service) Slots(ctx context.Context) view.AdSlots {
	configs, err := s.SlotConfigs(ctx, true)
	if err != nil || len(configs) == 0 {
		return nil
	}
	cfgByKey := make(map[string]AdSlot, len(configs))
	for _, cfg := range configs {
		cfgByKey[cfg.Key] = cfg
	}

	s.mu.Lock()
	var rows []Ad
	if s.cache != nil && time.Since(s.cacheAt) < 30*time.Second {
		rows = append([]Ad(nil), s.cache...)
	} else {
		now := time.Now().UTC()
		_ = s.db.WithContext(ctx).Where("status = ?", "active").
			Where("(start_at IS NULL OR start_at <= ?)", now).
			Where("(end_at IS NULL OR end_at > ?)", now).
			Order("priority DESC, created_at DESC").Limit(200).Find(&rows).Error
		s.cache = append([]Ad(nil), rows...)
		s.cacheAt = time.Now()
	}
	s.mu.Unlock()
	return pickSlots(rows, cfgByKey)
}

func pickSlots(rows []Ad, cfgByKey map[string]AdSlot) view.AdSlots {
	bySlot := make(map[string]map[string][]Ad)
	for _, ad := range rows {
		if _, ok := cfgByKey[ad.Slot]; !ok {
			continue
		}
		byFormat := bySlot[ad.Slot]
		if byFormat == nil {
			byFormat = make(map[string][]Ad)
			bySlot[ad.Slot] = byFormat
		}
		byFormat[ad.Format] = append(byFormat[ad.Format], ad)
	}
	slots := make(view.AdSlots)
	for slot, byFormat := range bySlot {
		cfg := cfgByKey[slot]
		selected := make([]Ad, 0, 4)
		for _, format := range []string{"text", "image", "html"} {
			list := byFormat[format]
			if len(list) == 0 {
				continue
			}
			limit := 1
			switch format {
			case "text":
				limit = cfg.MaxText
			case "image":
				limit = cfg.MaxImage
			}
			if limit <= 0 {
				continue
			}
			rand.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
			sort.SliceStable(list, func(i, j int) bool { return list[i].Priority > list[j].Priority })
			if len(list) > limit {
				list = list[:limit]
			}
			selected = append(selected, list...)
		}
		if len(selected) == 0 {
			continue
		}
		items := make([]view.AdView, 0, len(selected))
		for _, ad := range selected {
			item := view.AdView{
				ID: ad.ID, Format: ad.Format, Title: ad.Title, Text: ad.Text,
				URL: ad.URL, ImageURL: ad.ImageURL, Height: ad.Height,
			}
			if ad.Format == "html" {
				item.HTML = template.HTML(ad.HTML)
			}
			if item.Height <= 0 {
				item.Height = cfg.Height
			}
			items = append(items, item)
		}
		slots[slot] = items
	}
	return slots
}

func (s *Service) ListUser(ctx context.Context, userID string) ([]Ad, error) {
	var rows []Ad
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Limit(50).Find(&rows).Error
	return rows, err
}

// ListUserPage 分页返回某用户的广告及总数。
func (s *Service) ListUserPage(ctx context.Context, userID string, limit, offset int) ([]Ad, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Model(&Ad{}).Where("user_id = ?", userID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Ad
	err := base.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

// ListAdminPage 按状态、广告位筛选后分页返回广告及总数。
func (s *Service) ListAdminPage(ctx context.Context, status, slot string, limit, offset int) ([]Ad, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Model(&Ad{})
	if status != "" {
		base = base.Where("status = ?", status)
	}
	if slot != "" {
		base = base.Where("slot = ?", slot)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Ad
	err := base.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *Service) Get(ctx context.Context, id string) (*Ad, error) {
	var ad Ad
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &ad, nil
}

type PurchaseInput struct {
	UserID    string
	Slot      string
	Format    string
	Title     string
	Text      string
	URL       string
	ImageURL  string
	Months    int
	ChannelID string
	ClientIP  string
}

// Purchase creates a pending ad and a USD payment order. paid=true means the
// order settled immediately (for example with store balance).
func (s *Service) Purchase(ctx context.Context, in PurchaseInput) (*Ad, string, string, bool, error) {
	cfg, err := s.SlotByKey(ctx, in.Slot)
	if err != nil {
		return nil, "", "", false, httpx.Invalid(map[string]string{"slot": "广告位置无效"})
	}
	if !cfg.Enabled || !cfg.SellEnabled {
		return nil, "", "", false, httpx.Forbidden("ads.disabled", "该广告位暂未开放购买")
	}
	if !s.SelfServeEnabled() {
		return nil, "", "", false, httpx.Forbidden("ads.disabled", "自助推广暂未开放")
	}
	if in.Format != "text" && in.Format != "image" {
		return nil, "", "", false, httpx.Invalid(map[string]string{"format": "自助广告仅支持文字或图片"})
	}
	if in.Format == "text" && cfg.MaxText <= 0 {
		return nil, "", "", false, httpx.Forbidden("ads.capacity", "该广告位文字广告暂不可购买")
	}
	if in.Format == "image" && cfg.MaxImage <= 0 {
		return nil, "", "", false, httpx.Forbidden("ads.capacity", "该广告位图片广告暂不可购买")
	}
	if in.Months < 1 || in.Months > s.MaxMonths() {
		return nil, "", "", false, httpx.Invalid(map[string]string{"months": "投放月数超出范围"})
	}
	price := cfg.PriceText
	if in.Format == "image" {
		price = cfg.PriceImage
	}
	if price <= 0 {
		return nil, "", "", false, httpx.Forbidden("ads.price", "该广告位暂未开放购买")
	}
	title := strings.TrimSpace(in.Title)
	link := strings.TrimSpace(in.URL)
	if title == "" || link == "" {
		return nil, "", "", false, httpx.Invalid(map[string]string{"title": "请填写广告标题与链接"})
	}
	if !validURL(link) {
		return nil, "", "", false, httpx.Invalid(map[string]string{"url": "链接必须是 http/https 或站内路径"})
	}
	if in.Format == "text" && strings.TrimSpace(in.Text) == "" {
		return nil, "", "", false, httpx.Invalid(map[string]string{"text": "请填写广告文案"})
	}
	image := strings.TrimSpace(in.ImageURL)
	if in.Format == "image" && image == "" {
		return nil, "", "", false, httpx.Invalid(map[string]string{"image_url": "请填写图片地址"})
	}
	if image != "" && !validURL(image) {
		return nil, "", "", false, httpx.Invalid(map[string]string{"image_url": "图片地址无效"})
	}
	if s.billing == nil {
		return nil, "", "", false, httpx.NewError("ads.payment", 503, "支付服务暂不可用")
	}
	ad := &Ad{
		UserID: in.UserID, Slot: cfg.Key, Format: in.Format, Title: title,
		Text: strings.TrimSpace(in.Text), URL: link, ImageURL: image,
		Height: cfg.Height, Status: "pending_payment", Months: in.Months, Price: price * int64(in.Months),
	}
	if err := s.db.WithContext(ctx).Create(ad).Error; err != nil {
		return nil, "", "", false, err
	}
	tradeNo, payURL, paid, err := s.billing.CreateAdOrder(ctx, in.UserID, ad.ID, in.ChannelID,
		"广告投放 - "+cfg.Name+" - "+title, ad.Price, "USD", in.ClientIP)
	if err != nil {
		_ = s.db.WithContext(ctx).Delete(&Ad{}, "id = ?", ad.ID).Error
		return nil, "", "", false, err
	}
	if paid {
		if err := s.Fulfill(ctx, ad.ID); err != nil {
			return ad, tradeNo, "", true, err
		}
		if fresh, err := s.Get(ctx, ad.ID); err == nil {
			ad = fresh
		}
	}
	return ad, tradeNo, payURL, paid, nil
}

type AdminInput struct {
	ID       string
	Slot     string
	Format   string
	Title    string
	Text     string
	URL      string
	ImageURL string
	HTML     string
	Height   int
	Priority int
	Status   string
	Months   int
	StartAt  *time.Time
	EndAt    *time.Time
	Note     string
}

func (s *Service) SaveAdmin(ctx context.Context, in AdminInput) (*Ad, error) {
	cfg, err := s.SlotByKey(ctx, in.Slot)
	if err != nil {
		return nil, httpx.Invalid(map[string]string{"slot": "广告位置无效"})
	}
	if in.Height <= 0 {
		in.Height = cfg.Height
	}
	if in.Format != "text" && in.Format != "image" && in.Format != "html" {
		return nil, httpx.Invalid(map[string]string{"format": "广告格式无效"})
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, httpx.Invalid(map[string]string{"title": "标题不能为空"})
	}
	if in.Format != "html" && !validURL(strings.TrimSpace(in.URL)) {
		return nil, httpx.Invalid(map[string]string{"url": "链接必须是 http/https 或站内路径"})
	}
	if in.Format == "image" && strings.TrimSpace(in.ImageURL) == "" {
		return nil, httpx.Invalid(map[string]string{"image_url": "请填写图片地址"})
	}
	if in.Format == "html" && strings.TrimSpace(in.HTML) == "" {
		return nil, httpx.Invalid(map[string]string{"html": "请填写广告 HTML"})
	}
	status := in.Status
	switch status {
	case "active", "pending_review", "paused", "banned":
	default:
		status = "active"
	}
	months := in.Months
	if months < 1 {
		months = 1
	}
	now := time.Now().UTC()
	start, end := in.StartAt, in.EndAt
	if status == "active" && start == nil {
		start = &now
	}
	if status == "active" && end == nil {
		e := now.AddDate(0, months, 0)
		end = &e
	}
	row := Ad{
		Slot: in.Slot, Format: in.Format, Title: title, Text: strings.TrimSpace(in.Text),
		URL: strings.TrimSpace(in.URL), ImageURL: strings.TrimSpace(in.ImageURL), HTML: in.HTML,
		Height: in.Height, Priority: in.Priority, Status: status, Months: months,
		StartAt: start, EndAt: end, ReviewNote: strings.TrimSpace(in.Note),
	}
	if in.ID == "" {
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return nil, err
		}
		s.invalidate()
		return &row, nil
	}
	var existing Ad
	if err := s.db.WithContext(ctx).First(&existing, "id = ?", in.ID).Error; err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Model(&Ad{}).Where("id = ?", in.ID).Updates(map[string]any{
		"slot": row.Slot, "format": row.Format, "title": row.Title, "text": row.Text,
		"url": row.URL, "image_url": row.ImageURL, "html": row.HTML,
		"height": row.Height, "priority": row.Priority, "status": row.Status,
		"months": row.Months, "start_at": row.StartAt, "end_at": row.EndAt, "review_note": row.ReviewNote,
	}).Error
	if err != nil {
		return nil, err
	}
	s.invalidate()
	return s.Get(ctx, in.ID)
}

func (s *Service) SetStatus(ctx context.Context, id, status, note string) error {
	var ad Ad
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", id).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	updates := map[string]any{"status": status, "review_note": strings.TrimSpace(note)}
	switch status {
	case "active":
		if ad.StartAt == nil {
			updates["start_at"] = now
		}
		if ad.EndAt == nil || ad.EndAt.Before(now) {
			months := ad.Months
			if months < 1 {
				months = 1
			}
			updates["end_at"] = now.AddDate(0, months, 0)
		}
	case "paused", "banned", "rejected":
	case "pending_review":
	default:
		return httpx.Invalid(map[string]string{"status": "状态无效"})
	}
	if err := s.db.WithContext(ctx).Model(&Ad{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	s.invalidate()
	return nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.db.WithContext(ctx).Delete(&Ad{}, "id = ?", id).Error; err != nil {
		return err
	}
	s.invalidate()
	return nil
}

func (s *Service) ActiveByID(ctx context.Context, id string) (*Ad, error) {
	now := time.Now().UTC()
	var ad Ad
	err := s.db.WithContext(ctx).First(&ad, "id = ? AND status = ?", id, "active").Error
	if err != nil {
		return nil, err
	}
	if ad.StartAt != nil && ad.StartAt.After(now) {
		return nil, gorm.ErrRecordNotFound
	}
	if ad.EndAt != nil && !ad.EndAt.After(now) {
		return nil, gorm.ErrRecordNotFound
	}
	return &ad, nil
}

func (s *Service) Click(ctx context.Context, id string) (*Ad, error) {
	ad, err := s.ActiveByID(ctx, id)
	if err != nil {
		return nil, err
	}
	_ = s.db.WithContext(ctx).Model(&Ad{}).Where("id = ?", id).UpdateColumn("click_count", gorm.Expr("click_count + 1")).Error
	return ad, nil
}

func validURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "/")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// CacheStats reports the ad rotation cache state for the admin system page.
func (s *Service) CacheStats() (int, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		return 0, 0, false
	}
	return len(s.cache), time.Since(s.cacheAt), true
}
