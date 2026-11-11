package ads

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/authn"
	"goforum/internal/platform/httpx"
)

func (m *Module) promotePage(c *gin.Context) {
	data := m.base(c, "自助推广")
	data.Robots = "noindex,nofollow"
	data.Data = m.promoteDataFor(c, c.Query("error"))
	m.renderer.Render(c, http.StatusOK, "promote", data)
}

func (m *Module) purchase(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	months, _ := strconv.Atoi(c.PostForm("months"))
	_, tradeNo, payURL, paid, err := m.svc.Purchase(c.Request.Context(), PurchaseInput{
		UserID: user.ID, Slot: c.PostForm("slot"), Format: c.PostForm("format"),
		Title: c.PostForm("title"), Text: c.PostForm("text"), URL: c.PostForm("url"),
		ImageURL: c.PostForm("image_url"), Months: months,
		ChannelID: c.PostForm("channel_id"), ClientIP: c.ClientIP(),
	})
	if err != nil {
		data := m.base(c, "自助推广")
		data.Robots = "noindex,nofollow"
		data.Data = m.promoteDataFor(c, httpx.ErrorOf(err).Message)
		m.renderer.Render(c, http.StatusUnprocessableEntity, "promote", data)
		return
	}
	if paid {
		c.Redirect(http.StatusFound, "/promote?success=1")
		return
	}
	if payURL != "" {
		c.Redirect(http.StatusFound, payURL)
		return
	}
	c.Redirect(http.StatusFound, "/checkout/"+tradeNo)
}

func (m *Module) click(c *gin.Context) {
	ad, err := m.svc.Click(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	target := ad.URL
	if target == "" {
		target = "/"
	}
	c.Redirect(http.StatusFound, target)
}

func (m *Module) frame(c *gin.Context) {
	ad, err := m.svc.ActiveByID(c.Request.Context(), c.Param("id"))
	if err != nil || ad.Format != "html" {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Security-Policy", "default-src 'none'; img-src * data:; script-src * 'unsafe-inline' 'unsafe-eval'; style-src * 'unsafe-inline'; connect-src *; frame-src *; font-src * data:")
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer-when-downgrade")
	doc := "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"></head><body style=\"margin:0\">" + ad.HTML + "</body></html>"
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(doc))
}

func (m *Module) adminPage(c *gin.Context) {
	status := c.Query("status")
	if !adminStatuses[status] {
		status = ""
	}
	slot := c.Query("slot")
	if _, ok := PlacementByKey(slot); !ok {
		slot = ""
	}
	const perPage = 20
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	rows, total, err := m.svc.ListAdminPage(c.Request.Context(), status, slot, perPage, (page-1)*perPage)
	if err != nil {
		m.log.Error("list ads failed", "error", err)
	}
	var editing *Ad
	if id := c.Query("edit"); id != "" {
		if ad, err := m.svc.Get(c.Request.Context(), id); err == nil {
			editing = ad
		}
	}
	slots, _ := m.svc.SlotConfigs(c.Request.Context(), false)
	data := m.base(c, "广告管理")
	data.Data = adminData{
		Ads: rows, Editing: editing, Slots: slots,
		Status: status, Slot: slot, Total: total, Page: page,
		TotalPages: totalPages(total, perPage),
		BasePath:   "/admin/ads?status=" + url.QueryEscape(status) + "&slot=" + url.QueryEscape(slot),
		Error:      c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_ads", data)
}

func (m *Module) adminSave(c *gin.Context) {
	height, _ := strconv.Atoi(c.PostForm("height"))
	priority, _ := strconv.Atoi(c.PostForm("priority"))
	months, _ := strconv.Atoi(c.PostForm("months"))
	_, err := m.svc.SaveAdmin(c.Request.Context(), AdminInput{
		ID: c.PostForm("id"), Slot: c.PostForm("slot"), Format: c.PostForm("format"),
		Title: c.PostForm("title"), Text: c.PostForm("text"), URL: c.PostForm("url"),
		ImageURL: c.PostForm("image_url"), HTML: c.PostForm("html"),
		Height: height, Priority: priority, Status: c.PostForm("status"),
		Months: months, StartAt: parseDate(c.PostForm("start_at")), EndAt: parseDate(c.PostForm("end_at")),
		Note: c.PostForm("note"),
	})
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/ads?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/ads")
}

func (m *Module) adminAction(c *gin.Context) {
	id := c.PostForm("id")
	action := c.PostForm("action")
	switch action {
	case "delete":
		_ = m.svc.Delete(c.Request.Context(), id)
	case "approve", "activate":
		_ = m.svc.SetStatus(c.Request.Context(), id, "active", c.PostForm("note"))
	case "pending":
		_ = m.svc.SetStatus(c.Request.Context(), id, "pending_review", c.PostForm("note"))
	case "pause":
		_ = m.svc.SetStatus(c.Request.Context(), id, "paused", c.PostForm("note"))
	case "ban":
		_ = m.svc.SetStatus(c.Request.Context(), id, "banned", c.PostForm("note"))
	case "reject":
		_ = m.svc.SetStatus(c.Request.Context(), id, "rejected", c.PostForm("note"))
	}
	c.Redirect(http.StatusFound, "/admin/ads")
}

func (m *Module) settingsPage(c *gin.Context) {
	data := m.base(c, "广告设置")
	data.Data = settingsData{
		Enabled: m.svc.SelfServeEnabled(), Review: m.settings.GetBool("ads.self_serve.review"),
		MaxMonths: m.svc.MaxMonths(), Saved: c.Query("saved") == "1", Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_ads_settings", data)
}

func (m *Module) settingsSave(c *gin.Context) {
	actor := authn.FromContext(c.Request.Context()).ID
	maxMonths, _ := strconv.Atoi(c.PostForm("max_months"))
	if maxMonths < 1 || maxMonths > 36 {
		c.Redirect(http.StatusFound, "/admin/ads/settings?error="+url.QueryEscape("最长月数需在 1–36 之间"))
		return
	}
	values := map[string]any{
		"ads.self_serve.enabled":    c.PostForm("enabled") != "",
		"ads.self_serve.review":     c.PostForm("review") != "",
		"ads.self_serve.max_months": maxMonths,
	}
	for key, value := range values {
		if err := m.settings.Set(c.Request.Context(), key, value, actor); err != nil {
			c.Redirect(http.StatusFound, "/admin/ads/settings?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
			return
		}
	}
	c.Redirect(http.StatusFound, "/admin/ads/settings?saved=1")
}

func (m *Module) slotsPage(c *gin.Context) {
	slots, _ := m.svc.SlotConfigs(c.Request.Context(), false)
	var editing *AdSlot
	if id := c.Query("edit"); id != "" {
		for i := range slots {
			if slots[i].ID == id {
				editing = &slots[i]
				break
			}
		}
	}
	data := m.base(c, "广告位管理")
	data.Data = slotsData{Slots: slots, Editing: editing, Saved: c.Query("saved") == "1", Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "admin_ads_slots", data)
}

func (m *Module) slotsSave(c *gin.Context) {
	slot, err := m.svc.SlotByKey(c.Request.Context(), c.PostForm("key"))
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/ads/slots?error="+url.QueryEscape("广告位不存在"))
		return
	}
	maxText, _ := strconv.Atoi(c.PostForm("max_text"))
	maxImage, _ := strconv.Atoi(c.PostForm("max_image"))
	if maxText < 0 || maxText > 10 {
		maxText = 0
	}
	if maxImage < 0 || maxImage > 10 {
		maxImage = 0
	}
	height, _ := strconv.Atoi(c.PostForm("height"))
	sortOrder, _ := strconv.Atoi(c.PostForm("sort"))
	updates := map[string]any{
		"name": strings.TrimSpace(c.PostForm("name")), "description": strings.TrimSpace(c.PostForm("description")),
		"height": height, "sort": sortOrder,
		"enabled": c.PostForm("enabled") != "", "sell_enabled": c.PostForm("sell_enabled") != "",
		"max_text": maxText, "max_image": maxImage,
		"price_text": parseUSD(c.PostForm("price_text")), "price_image": parseUSD(c.PostForm("price_image")),
	}
	if updates["name"] == "" || height < 0 {
		c.Redirect(http.StatusFound, "/admin/ads/slots?error="+url.QueryEscape("名称和高度不能为空"))
		return
	}
	if err := m.svc.UpdateSlot(c.Request.Context(), slot.ID, updates); err != nil {
		c.Redirect(http.StatusFound, "/admin/ads/slots?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/ads/slots?saved=1")
}

func (m *Module) slotsAction(c *gin.Context) {
	slot, err := m.svc.SlotByKey(c.Request.Context(), c.PostForm("key"))
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/ads/slots")
		return
	}
	switch c.PostForm("action") {
	case "toggle_visible":
		_ = m.svc.UpdateSlot(c.Request.Context(), slot.ID, map[string]any{"enabled": !slot.Enabled})
	case "toggle_sell":
		_ = m.svc.UpdateSlot(c.Request.Context(), slot.ID, map[string]any{"sell_enabled": !slot.SellEnabled})
	case "delete":
		if err := m.svc.DeleteSlot(c.Request.Context(), slot.ID); err != nil {
			c.Redirect(http.StatusFound, "/admin/ads/slots?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
			return
		}
	}
	c.Redirect(http.StatusFound, "/admin/ads/slots")
}

func parseUSD(value string) int64 {
	value = strings.TrimSpace(strings.TrimPrefix(value, "$"))
	if value == "" {
		return 0
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || f < 0 {
		return 0
	}
	return int64(f*100 + 0.5)
}

func parseDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}
