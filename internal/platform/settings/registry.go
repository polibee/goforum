package settings

import (
	"fmt"
	"sort"
	"sync"
)

type Registry struct {
	mu    sync.RWMutex
	defs  map[string]Definition
	order []string
}

func NewRegistry() *Registry {
	return &Registry{defs: make(map[string]Definition)}
}

func (r *Registry) Register(defs ...Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			return err
		}
		// A secret-typed setting is always sensitive. Deriving the flag here
		// means masking in the UI, audit log and revision history cannot be
		// forgotten at a definition site.
		if d.Type == TypeSecret {
			d.Secret = true
		}
		full := d.FullKey()
		if _, exists := r.defs[full]; exists {
			return fmt.Errorf("duplicate setting %q", full)
		}
		r.defs[full] = d
		r.order = append(r.order, full)
	}
	return nil
}

func (r *Registry) Get(key string) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[key]
	return d, ok
}

func (r *Registry) All() []Definition {
	r.mu.RLock()
	out := make([]Definition, 0, len(r.defs))
	for _, k := range r.order {
		out = append(out, r.defs[k])
	}
	r.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group == out[j].Group {
			return out[i].FullKey() < out[j].FullKey()
		}
		return out[i].Group < out[j].Group
	})
	return out
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.defs)
}

// PlatformDefinitions are owned by the platform and available before any module loads.
func PlatformDefinitions() []Definition {
	return []Definition{
		{Namespace: "site", Key: "general.name", Group: "general", Label: "站点名称", Type: TypeString, Default: "GoForum", Permission: "settings.site", Max: 60},
		{Namespace: "site", Key: "general.tagline", Group: "general", Label: "站点标语", Type: TypeString, Default: "一个模块化社区", Permission: "settings.site", Max: 120},
		{Namespace: "site", Key: "general.description", Group: "general", Label: "站点描述", Type: TypeText, Default: "", Permission: "settings.site", Max: 500},
		{Namespace: "site", Key: "general.url", Group: "general", Label: "站点 URL", Type: TypeURL, Default: "", Permission: "settings.site"},
		{Namespace: "site", Key: "general.locale", Group: "general", Label: "默认语言", Type: TypeEnum, Default: "zh-CN", Enum: []string{"zh-CN", "en-US"}, Permission: "settings.site"},
		{Namespace: "site", Key: "general.timezone", Group: "general", Label: "默认时区", Type: TypeString, Default: "Asia/Shanghai", Permission: "settings.site"},
		{Namespace: "site", Key: "general.contact_email", Group: "general", Label: "联系邮箱", Type: TypeEmail, Default: "", Permission: "settings.site"},
		{Namespace: "site", Key: "general.announcement", Group: "general", Label: "全站公告", Type: TypeText, Default: "", Permission: "settings.site", Max: 500},

		{Namespace: "site", Key: "registration.mode", Group: "registration", Label: "注册模式", Type: TypeEnum, Default: "open", Enum: []string{"open", "invite", "approval", "closed"}, Permission: "settings.registration"},
		{Namespace: "site", Key: "registration.email_verification", Group: "verification", Label: "注册邮箱验证", Type: TypeBool, Default: true, Permission: "settings.registration", Description: "开启后，新注册用户需要点击验证邮件中的链接才能登录；关闭后注册成功即可直接登录。"},
		{Namespace: "site", Key: "registration.password_min", Group: "registration", Label: "密码最小长度", Type: TypeInt, Default: 10, Min: 6, Max: 128, Permission: "settings.registration"},
		{Namespace: "site", Key: "registration.invite_quota", Group: "registration", Label: "默认邀请名额", Type: TypeInt, Default: 3, Min: 0, Max: 100, Permission: "settings.registration"},

		{Namespace: "site", Key: "verification.password_reset", Group: "verification", Label: "允许找回密码", Type: TypeBool, Default: true, Permission: "settings.registration", Description: "关闭后，前台隐藏「忘记密码」入口，并拒绝重置密码请求。"},
		{Namespace: "site", Key: "verification.password_change", Group: "verification", Label: "允许用户修改密码", Type: TypeBool, Default: true, Permission: "settings.registration", Description: "关闭后，账号安全页隐藏修改密码表单，用户需联系管理员处理。"},
		{Namespace: "site", Key: "verification.password_change_verify", Group: "verification", Label: "修改密码需邮箱验证码", Type: TypeBool, Default: false, Permission: "settings.registration", Description: "开启后，用户修改密码时需先向注册邮箱发送并输入 6 位验证码，验证码 10 分钟内有效。"},
		{Namespace: "site", Key: "verification.email_change", Group: "verification", Label: "允许用户修改邮箱", Type: TypeBool, Default: true, Permission: "settings.registration", Description: "关闭后，账号安全页隐藏修改邮箱表单，邮箱只能由管理员调整。"},
		{Namespace: "site", Key: "verification.email_change_verify", Group: "verification", Label: "修改邮箱需邮件确认", Type: TypeBool, Default: true, Permission: "settings.registration", Description: "开启后，新邮箱需点击确认链接才生效；关闭后验证当前密码即可立即换绑。"},
		{Namespace: "site", Key: "verification.email_daily_limit", Group: "verification", Label: "单日邮件次数上限", Type: TypeInt, Default: 10, Min: 0, Max: 1000, Permission: "settings.registration", Description: "每个账号每天最多可触发的验证/安全邮件数量（注册验证、找回密码、修改密码、换绑邮箱）。0 表示不限制。"},
		{Namespace: "site", Key: "verification.email_total_limit", Group: "verification", Label: "累计邮件次数上限（永久）", Type: TypeInt, Default: 50, Min: 0, Max: 100000, Permission: "settings.registration", Description: "每个账号累计最多可触发的验证/安全邮件数量，不随日期重置。0 表示不限制。"},

		{Namespace: "site", Key: "security.admin_2fa_policy", Group: "security", Label: "管理员两步验证策略", Type: TypeEnum, Default: "superadmin", Enum: []string{"off", "superadmin", "admins"}, EnumLabels: map[string]string{"off": "关闭", "superadmin": "仅超管", "admins": "所有管理员"}, Permission: "settings.write", Description: "超管或所有后台管理员必须启用 TOTP 两步验证后才能进入后台；已启用两步验证的账号登录时始终需要验证码。"},

		{Namespace: "site", Key: "theme.brand_color", Group: "appearance", Label: "品牌主色", Type: TypeColor, Default: "#2563eb", Permission: "settings.site"},
		{Namespace: "site", Key: "theme.accent_color", Group: "appearance", Label: "强调色", Type: TypeColor, Default: "#0ea5e9", Permission: "settings.site"},
		{Namespace: "site", Key: "theme.color_mode", Group: "appearance", Label: "默认配色", Type: TypeEnum, Default: "system", Enum: []string{"light", "dark", "system"}, Permission: "settings.site"},
		{Namespace: "site", Key: "theme.preset", Group: "appearance", Label: "界面主题", Type: TypeEnum, Default: "default", Enum: []string{"default", "wechat", "douyin", "minimal"}, EnumLabels: map[string]string{"default": "默认风格", "wechat": "微信风格", "douyin": "抖音风格", "minimal": "简约白"}, Permission: "settings.site"},
		{Namespace: "site", Key: "theme.allow_user_toggle", Group: "appearance", Label: "允许用户切换主题", Type: TypeBool, Default: true, Permission: "settings.site"},
		{Namespace: "site", Key: "theme.logo_light", Group: "appearance", Label: "Logo（亮色）", Type: TypeString, Default: "", Permission: "settings.site"},
		{Namespace: "site", Key: "theme.logo_dark", Group: "appearance", Label: "Logo（暗色）", Type: TypeString, Default: "", Permission: "settings.site"},

		{Namespace: "site", Key: "nav.header", Group: "navigation", Label: "顶部导航", Type: TypeJSON, Default: []any{}, Permission: "settings.site"},
		{Namespace: "site", Key: "nav.footer", Group: "navigation", Label: "页脚链接", Type: TypeJSON, Default: []any{}, Permission: "settings.site"},
		{Namespace: "site", Key: "pages.footer_copyright", Group: "navigation", Label: "页脚版权", Type: TypeString, Default: "", Permission: "settings.site", Max: 200},

		{Namespace: "seo", Key: "title_template", Group: "seo", Label: "标题模板", Type: TypeString, Default: "{title} - {site}", Permission: "settings.seo", Max: 120},
		{Namespace: "seo", Key: "default_description", Group: "seo", Label: "默认描述", Type: TypeText, Default: "", Permission: "settings.seo", Max: 300},
		{Namespace: "seo", Key: "sitemap_enabled", Group: "seo", Label: "启用 Sitemap", Type: TypeBool, Default: true, Permission: "settings.seo"},
		{Namespace: "seo", Key: "nofollow_low_trust", Group: "seo", Label: "低信任用户外链 nofollow", Type: TypeBool, Default: true, Permission: "settings.seo"},
		{Namespace: "seo", Key: "og_image", Group: "seo", Label: "默认社交分享图", Type: TypeURL, Default: "", Permission: "settings.seo"},
		{Namespace: "seo", Key: "twitter", Group: "seo", Label: "Twitter / X 账号", Type: TypeString, Default: "", Permission: "settings.seo", Max: 40},
		{Namespace: "seo", Key: "robots_extra", Group: "seo", Label: "robots.txt 追加规则", Type: TypeText, Default: "", Permission: "settings.seo", Max: 2000},
		{Namespace: "seo", Key: "robots_custom", Group: "seo", Label: "自定义 robots.txt（完全覆盖）", Type: TypeText, Default: "", Permission: "settings.seo", Max: 20000},
		{Namespace: "seo", Key: "google_verification", Group: "seo", Label: "Google 验证码", Type: TypeString, Default: "", Permission: "settings.seo", Max: 120},
		{Namespace: "seo", Key: "bing_verification", Group: "seo", Label: "Bing 验证码", Type: TypeString, Default: "", Permission: "settings.seo", Max: 120},
		{Namespace: "seo", Key: "baidu_verification", Group: "seo", Label: "百度验证码", Type: TypeString, Default: "", Permission: "settings.seo", Max: 120},
		{Namespace: "seo", Key: "index_users", Group: "seo", Label: "允许索引用户主页", Type: TypeBool, Default: true, Permission: "settings.seo"},

		{Namespace: "mail", Key: "provider", Group: "notification", Label: "邮件发送方式", Type: TypeEnum, Default: "log", Enum: []string{"log", "smtp", "resend"}, Permission: "settings.mail"},
		{Namespace: "mail", Key: "from", Group: "notification", Label: "发件人", Type: TypeString, Default: "", Permission: "settings.mail", Max: 200},
		{Namespace: "mail", Key: "smtp.host", Group: "notification", Label: "SMTP 主机", Type: TypeString, Default: "", Permission: "settings.mail", Max: 200},
		{Namespace: "mail", Key: "smtp.port", Group: "notification", Label: "SMTP 端口", Type: TypeInt, Default: 587, Min: 1, Max: 65535, Permission: "settings.mail"},
		{Namespace: "mail", Key: "smtp.username", Group: "notification", Label: "SMTP 用户名", Type: TypeString, Default: "", Permission: "settings.mail", Max: 200},
		{Namespace: "mail", Key: "smtp.password", Group: "notification", Label: "SMTP 密码", Type: TypeSecret, Default: "", Permission: "settings.mail"},
		{Namespace: "mail", Key: "resend.api_key", Group: "notification", Label: "Resend API Key", Type: TypeSecret, Default: "", Permission: "settings.mail"},
		{Namespace: "mail", Key: "resend.base_url", Group: "notification", Label: "Resend API 地址", Type: TypeURL, Default: "https://api.resend.com", Permission: "settings.mail"},

		{Namespace: "billing", Key: "nowpayments.api_key", Group: "payment", Label: "NOWPayments API Key", Type: TypeSecret, Default: "", Permission: "settings.payment", RegisterURL: "https://account.nowpayments.io/create-account?link_id=3940543227"},
		{Namespace: "billing", Key: "nowpayments.ipn_secret", Group: "payment", Label: "NOWPayments IPN Secret", Type: TypeSecret, Default: "", Permission: "settings.payment"},
		{Namespace: "billing", Key: "nowpayments.base_url", Group: "payment", Label: "NOWPayments API 地址", Type: TypeURL, Default: "https://api.nowpayments.io/v1", Permission: "settings.payment"},
		{Namespace: "billing", Key: "nowpayments.pay_currency", Group: "payment", Label: "NOWPayments 支付币种（留空让用户选择）", Type: TypeString, Default: "", Permission: "settings.payment", Max: 20},
		{Namespace: "billing", Key: "xcash.appid", Group: "payment", Label: "XCash AppID", Type: TypeString, Default: "", Permission: "settings.payment", Max: 64, RegisterURL: "https://dash.xca.sh/register?ref=2GWV5MKT", Description: "在 XCash 控制台创建项目后获得，形如 XC-XXXXXXXX。"},
		{Namespace: "billing", Key: "xcash.hmac_key", Group: "payment", Label: "XCash HMAC Key", Type: TypeSecret, Default: "", Permission: "settings.payment"},
		{Namespace: "billing", Key: "xcash.base_url", Group: "payment", Label: "XCash API 地址", Type: TypeURL, Default: "https://pay.xca.sh", Permission: "settings.payment"},
		{Namespace: "billing", Key: "xcash.duration", Group: "payment", Label: "XCash 账单有效期（分钟）", Type: TypeInt, Default: 15, Min: 5, Max: 30, Permission: "settings.payment"},
		{Namespace: "billing", Key: "deipay.session_key", Group: "payment", Label: "Deipay Session Key", Type: TypeSecret, Default: "", Permission: "settings.payment"},
		{Namespace: "billing", Key: "deipay.base_url", Group: "payment", Label: "Deipay API 地址", Type: TypeURL, Default: "https://api.depipay.com", Permission: "settings.payment"},
		{Namespace: "billing", Key: "deipay.chain_id", Group: "payment", Label: "Deipay 链 ID", Type: TypeInt, Default: 1, Min: 1, Max: 1000000, Permission: "settings.payment", Hidden: true},
		{Namespace: "billing", Key: "deipay.tokens", Group: "payment", Label: "Deipay 接收代币", Type: TypeText, Default: "1:0xdac17f958d2ee523a2206206994597c13d831ec7", Permission: "settings.payment", Max: 500, Hidden: true},
		{Namespace: "billing", Key: "deipay.deadline_secs", Group: "payment", Label: "Deipay 发票有效期（秒）", Type: TypeInt, Default: 86400, Min: 300, Max: 604800, Permission: "settings.payment"},
		{Namespace: "billing", Key: "creem.api_key", Group: "payment", Label: "Creem API Key", Type: TypeSecret, Default: "", Permission: "settings.payment"},
		{Namespace: "billing", Key: "creem.webhook_secret", Group: "payment", Label: "Creem Webhook Secret", Type: TypeSecret, Default: "", Permission: "settings.payment"},
		{Namespace: "billing", Key: "creem.base_url", Group: "payment", Label: "Creem API 地址", Type: TypeURL, Default: "https://api.creem.io/v1", Permission: "settings.payment"},
		{Namespace: "billing", Key: "creem.products", Group: "payment", Label: "Creem 商品映射（slug → product_id）", Type: TypeJSON, Default: map[string]any{}, Permission: "settings.payment"},
		{Namespace: "billing", Key: "lemonsqueezy.api_key", Group: "payment", Label: "Lemon Squeezy API Key", Type: TypeSecret, Default: "", Permission: "settings.payment"},
		{Namespace: "billing", Key: "lemonsqueezy.webhook_secret", Group: "payment", Label: "Lemon Squeezy Webhook Secret", Type: TypeSecret, Default: "", Permission: "settings.payment"},
		{Namespace: "billing", Key: "lemonsqueezy.base_url", Group: "payment", Label: "Lemon Squeezy API 地址", Type: TypeURL, Default: "https://api.lemonsqueezy.com/v1", Permission: "settings.payment"},
		{Namespace: "billing", Key: "lemonsqueezy.store_id", Group: "payment", Label: "Lemon Squeezy Store ID", Type: TypeString, Default: "", Permission: "settings.payment", Max: 40},
		{Namespace: "billing", Key: "lemonsqueezy.variants", Group: "payment", Label: "Lemon Squeezy 变体映射（slug → variant_id）", Type: TypeJSON, Default: map[string]any{}, Permission: "settings.payment"},

		{Namespace: "platform", Key: "maintenance.enabled", Group: "maintenance", Label: "维护模式", Type: TypeBool, Default: false, Permission: "settings.write", Restart: false},
		{Namespace: "platform", Key: "maintenance.message", Group: "maintenance", Label: "维护说明", Type: TypeText, Default: "", Permission: "settings.write"},
		{Namespace: "platform", Key: "integration.analytics", Group: "integrations", Label: "统计方案", Type: TypeEnum, Default: "none", Enum: []string{"none", "google", "plausible"}, Permission: "settings.integration"},
	}
}
