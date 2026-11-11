# 05 API 设计

## 1. 总体约定

| 项 | 约定 |
|---|---|
| 基础路径 | `/api/v1` |
| 页面路由 | 无前缀（服务端渲染），见 4.1 |
| 数据格式 | `application/json; charset=utf-8`，字段 `snake_case` |
| 时间 | RFC 3339，UTC，如 `2026-11-08T10:00:00Z` |
| ID | ULID 字符串（26 位） |
| 金额 | `amount_minor`（整数）+ `currency`（ISO 4217） |
| 枚举 | 小写下划线，稳定不随文案变化 |
| 空值 | 缺省字段可省略；显式置空用 `null` |
| 幂等 | 写操作支持 `Idempotency-Key` 头（可选但推荐） |
| 版本 | 路径版本；破坏性变更升 `/api/v2`，旧版本至少保留 6 个月 |

### 1.1 成功信封

```json
{
  "data": { "...": "..." },
  "meta": { "request_id": "01J..." }
}
```

列表：

```json
{
  "data": [ { "...": "..." } ],
  "meta": {
    "request_id": "01J...",
    "page": 2,
    "per_page": 20,
    "total": 1532,
    "has_next": true
  }
}
```

游标：

```json
{
  "data": [ "..." ],
  "meta": { "next_cursor": "01J...", "has_next": true }
}
```

### 1.2 错误信封

```json
{
  "error": {
    "code": "topic.not_found",
    "message": "主题不存在或已被删除",
    "fields": { "title": "标题不能为空" },
    "request_id": "01J..."
  }
}
```

- `message` 面向用户，可直接展示。
- `fields` 仅表单校验错误出现。
- `request_id` 用于日志排查，必须返回。

## 2. 鉴权

### 2.1 浏览器（Cookie 会话）

- 登录成功下发 `gf_session`（HttpOnly、Secure、SameSite=Lax）。
- 写请求必须携带 CSRF Token：页面 `<meta name="csrf-token">`，请求头 `X-CSRF-Token`。
- 会话固定防护：登录后轮换会话 ID。
- 空闲超时与绝对过期可配置；封禁立即失效。

### 2.2 API Token（Bearer）

```
Authorization: Bearer gf_pat_xxxxxxxx
```

- Token 只在创建时显示一次，数据库存哈希。
- 支持 scope：`read`、`write`、`admin`（按需细分）。
- 可设置过期时间与最后使用时间；泄露可单独撤销。

### 2.3 权限与错误

| 场景 | HTTP | 错误码 |
|---|---|---|
| 未登录 | 401 | `auth.required` |
| Token 无效/过期 | 401 | `auth.invalid_token` |
| 无权限 | 403 | `permission.denied` |
| 资源不存在或不可见 | 404 | `<type>.not_found`（避免探测私有资源） |
| 被禁言 | 403 | `user.suspended` |
| 账号封禁 | 403 | `user.banned` |
| 需要 2FA | 401 | `auth.2fa_required` |

## 3. 通用能力

### 3.1 分页

| 场景 | 方式 | 参数 |
|---|---|---|
| 主题/回复/通知信息流 | 游标 | `cursor`、`limit`（默认 20，最大 100） |
| 后台列表、搜索结果 | 页码 | `page`、`per_page`（默认 20，最大 100） |

游标为不透明字符串，禁止客户端解析；游标失效返回 `pagination.invalid_cursor`。

### 3.2 筛选与排序

```
GET /api/v1/topics?category=tech&tag=golang&sort=latest&status=open&author=01J...
```

- `sort`：`latest`（默认）、`hot`、`top`、`unread`、`created`。
- 未知参数忽略；非法枚举返回 `validation.invalid`。
- 筛选组合必须命中索引，否则返回 `validation.too_broad`（可配置）。

### 3.3 限流

| 范围 | 默认 | 响应头 |
|---|---|---|
| 匿名读 | 300 次/分钟/IP | `X-RateLimit-Limit/Remaining/Reset` |
| 登录用户读 | 600 次/分钟/用户 | 同上 |
| 写操作 | 60 次/分钟/用户 | 同上 |
| 登录 | 10 次/15 分钟/IP + 账号 | `Retry-After` |
| 注册 | 5 次/小时/IP | `Retry-After` |
| 上传 | 30 次/小时/用户 | 同上 |
| 支付下单 | 10 次/10 分钟/用户 | 同上 |
| 回调 | 按渠道单独配置 | 同上 |

超限返回 `429`，错误码 `rate.limited`。

### 3.4 幂等

- 写操作支持 `Idempotency-Key`（客户端生成，24 小时内有效）。
- 服务端记录 `(user_id, key)` 与首次响应；重复请求返回首次结果。
- 支付、发帖、点赞、退款必须实现幂等。

## 4. 路由总表

### 4.1 页面路由（服务端渲染）

| 方法 | 路径 | 页面 | 权限 |
|---|---|---|---|
| GET | `/` | 首页信息流 | 公开 |
| GET | `/c/:slug` | 板块页 | 板块可见 |
| GET | `/tags/:slug` | 标签页 | 公开 |
| GET | `/t/:id-:slug` | 主题详情 | 主题可见 |
| GET | `/new` | 发帖（可带 `?category=`） | `topic.create` |
| GET | `/t/:id/edit` | 编辑主题 | 作者/版主 |
| GET | `/posts/:id/edit` | 编辑回复 | 作者/版主 |
| GET | `/search` | 搜索页 | 公开（可配置） |
| GET | `/u/:username` | 用户主页 | 公开/隐私设置 |
| GET | `/u/:username/:tab` | 主题/回复/点赞/收藏/徽章 | 同上 |
| GET | `/moderation/blacklist` | 小黑屋公示 | 公开（可配置） |
| GET | `/notifications` | 通知中心 | 登录 |
| GET | `/messages` | 私信列表 | 登录 |
| GET | `/messages/:id` | 会话 | 参与者 |
| GET | `/login` | 登录 | 游客 |
| GET | `/register` | 注册 | 游客（按注册模式） |
| GET | `/invite/:code` | 邀请注册 | 游客 |
| GET | `/forgot-password` | 找回密码 | 游客 |
| GET | `/reset-password/:token` | 重置密码 | 游客 |
| GET | `/verify-email/:token` | 邮箱验证 | 游客 |
| GET | `/settings/profile` | 资料设置 | 登录 |
| GET | `/settings/security` | 安全设置 | 登录 |
| GET | `/settings/notifications` | 通知偏好 | 登录 |
| GET | `/settings/preferences` | 偏好设置 | 登录 |
| GET | `/settings/tokens` | API Token | 登录 |
| GET | `/pricing` | 套餐与充值 | 公开 |
| GET | `/checkout/:out_trade_no` | 收银台 | 订单所有者 |
| GET | `/billing/orders` | 我的订单 | 登录 |
| GET | `/billing/subscription` | 我的订阅 | 登录 |
| GET | `/points` | 积分与等级 | 登录 |
| GET | `/invites` | 邀请管理 | 登录 |
| GET | `/lotteries` `/lotteries/:id` | 抽奖 | 公开/登录 |
| GET | `/admin` | 后台概览 | `admin.access` |
| GET | `/admin/*` | 后台各页 | 对应权限键 |
| GET | `/sitemap.xml` `/sitemap-*.xml` | Sitemap | 公开 |
| GET | `/robots.txt` | robots | 公开 |
| GET | `/feed` `/c/:slug/feed` `/u/:username/feed` | RSS/Atom | 公开 |

### 4.2 认证与账号

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/auth/register` | 注册（开放/邀请码/审核） |
| POST | `/api/v1/auth/login` | 登录，返回会话或 2FA 挑战 |
| POST | `/api/v1/auth/logout` | 退出并失效会话 |
| POST | `/api/v1/auth/password/forgot` | 发送重置邮件 |
| POST | `/api/v1/auth/password/reset` | 重置密码 |
| POST | `/api/v1/auth/email/verify` | 验证邮箱 |
| POST | `/api/v1/auth/email/resend` | 重发验证邮件 |
| POST | `/api/v1/auth/2fa/challenge` | 校验 TOTP |
| POST | `/api/v1/auth/2fa/enable` | 启用 2FA（返回密钥与恢复码） |
| POST | `/api/v1/auth/2fa/disable` | 关闭 2FA |
| GET | `/api/v1/auth/oauth/:provider` | 跳转第三方 |
| GET | `/api/v1/auth/oauth/:provider/callback` | 回调 |
| GET | `/api/v1/me` | 当前用户 |
| PATCH | `/api/v1/me` | 更新资料 |
| GET | `/api/v1/me/settings` | 偏好 |
| PATCH | `/api/v1/me/settings` | 更新偏好 |
| GET | `/api/v1/me/sessions` | 设备列表 |
| DELETE | `/api/v1/me/sessions/:id` | 下线设备 |
| GET | `/api/v1/me/tokens` | API Token 列表 |
| POST | `/api/v1/me/tokens` | 创建 Token |
| DELETE | `/api/v1/me/tokens/:id` | 撤销 Token |
| POST | `/api/v1/me/export` | 导出个人数据（P2） |
| DELETE | `/api/v1/me` | 注销账号（含冷静期） |

### 4.3 用户

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/users/:id` | 用户公开资料 |
| GET | `/api/v1/users/:id/topics` | 用户主题 |
| GET | `/api/v1/users/:id/posts` | 用户回复 |
| GET | `/api/v1/users/:id/badges` | 徽章 |
| POST | `/api/v1/users/:id/follow` | 关注 |
| DELETE | `/api/v1/users/:id/follow` | 取消关注 |
| POST | `/api/v1/users/:id/block` | 屏蔽 |
| DELETE | `/api/v1/users/:id/block` | 取消屏蔽 |

### 4.4 板块、标签、主题、回复

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/categories` | 板块树 |
| GET | `/api/v1/categories/:slug` | 板块详情 |
| GET | `/api/v1/categories/:slug/topics` | 板块主题 |
| GET | `/api/v1/tags` | 标签列表/搜索 |
| GET | `/api/v1/tags/:slug/topics` | 标签主题 |
| GET | `/api/v1/topics` | 信息流（游标） |
| POST | `/api/v1/topics` | 发主题（含首楼） |
| GET | `/api/v1/topics/:id` | 主题详情 |
| PATCH | `/api/v1/topics/:id` | 编辑标题/板块/标签 |
| DELETE | `/api/v1/topics/:id` | 删除主题 |
| POST | `/api/v1/topics/:id/status` | 置顶/精华/锁定/归档/解决 |
| POST | `/api/v1/topics/:id/move` | 移动板块 |
| POST | `/api/v1/topics/:id/merge` | 合并主题 |
| POST | `/api/v1/topics/:id/split` | 拆分为新主题 |
| GET | `/api/v1/topics/:id/posts` | 楼层（游标） |
| POST | `/api/v1/topics/:id/posts` | 回复 |
| GET | `/api/v1/posts/:id` | 单条回复 |
| PATCH | `/api/v1/posts/:id` | 编辑回复 |
| DELETE | `/api/v1/posts/:id` | 删除回复 |
| GET | `/api/v1/posts/:id/revisions` | 编辑历史 |
| POST | `/api/v1/posts/:id/reactions` | 点赞/表情 |
| DELETE | `/api/v1/posts/:id/reactions/:reaction` | 取消 |
| POST | `/api/v1/topics/:id/poll/vote` | 投票 |
| DELETE | `/api/v1/topics/:id/poll/vote` | 取消投票 |
| GET | `/api/v1/topics/:id/similar` | 相似主题 |
| POST | `/api/v1/read-states` | 上报阅读进度 |

### 4.5 互动、通知、私信

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/bookmarks` | 收藏 |
| DELETE | `/api/v1/bookmarks/:id` | 取消收藏 |
| GET | `/api/v1/bookmarks` | 我的收藏 |
| POST | `/api/v1/subscriptions` | 关注主题/板块/标签 |
| DELETE | `/api/v1/subscriptions/:id` | 取消关注 |
| GET | `/api/v1/notifications` | 通知列表（游标） |
| POST | `/api/v1/notifications/read` | 全部已读 |
| POST | `/api/v1/notifications/:id/read` | 单条已读 |
| GET | `/api/v1/notifications/stream` | SSE |
| GET | `/api/v1/notifications/preferences` | 偏好 |
| PATCH | `/api/v1/notifications/preferences` | 更新偏好 |
| GET | `/api/v1/conversations` | 会话列表 |
| POST | `/api/v1/conversations` | 新建会话 |
| GET | `/api/v1/conversations/:id` | 会话详情 |
| POST | `/api/v1/conversations/:id/messages` | 发送消息 |
| POST | `/api/v1/conversations/:id/read` | 标记已读 |
| POST | `/api/v1/conversations/:id/block` | 屏蔽会话 |

### 4.6 举报与审核

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/flags` | 举报内容/用户 |
| GET | `/api/v1/me/flags` | 我的举报 |
| GET | `/api/v1/admin/review` | 审核队列 |
| POST | `/api/v1/admin/review/:id/claim` | 领取 |
| POST | `/api/v1/admin/review/:id/decide` | 处理 |
| GET | `/api/v1/admin/flags` | 举报列表 |
| POST | `/api/v1/admin/flags/:id/resolve` | 处理举报 |
| POST | `/api/v1/admin/users/:id/suspend` | 禁言 |
| POST | `/api/v1/admin/users/:id/ban` | 封禁 |
| GET | `/api/v1/admin/audit-logs` | 审计 |

### 4.7 搜索、上传

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/search` | 搜索（`q`、`type`、`category`、`tag`、`author`、`sort`） |
| GET | `/api/v1/search/suggest` | 建议（标题/标签/用户） |
| GET | `/api/v1/search/hot` | 热词 |
| POST | `/api/v1/uploads` | 上传 |
| GET | `/api/v1/uploads/:id` | 附件元数据 |
| DELETE | `/api/v1/uploads/:id` | 删除未引用上传 |

### 4.8 运营与商业化

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/points` | 我的积分与等级 |
| GET | `/api/v1/points/transactions` | 积分流水 |
| POST | `/api/v1/checkins` | 签到 |
| GET | `/api/v1/checkins` | 签到记录 |
| GET | `/api/v1/invites` | 我的邀请 |
| POST | `/api/v1/invites` | 生成邀请码 |
| DELETE | `/api/v1/invites/:id` | 作废 |
| GET | `/api/v1/badges` | 徽章目录 |
| GET | `/api/v1/lotteries` | 抽奖列表 |
| POST | `/api/v1/lotteries/:id/entries` | 参与 |
| POST | `/api/v1/bounties` | 发布悬赏（冻结积分） |
| POST | `/api/v1/bounties/:id/settle` | 采纳结算 |
| GET | `/api/v1/products` | 商品列表 |
| POST | `/api/v1/orders` | 创建订单 |
| GET | `/api/v1/orders/:out_trade_no` | 订单状态 |
| POST | `/api/v1/orders/:id/close` | 关闭订单 |
| GET | `/api/v1/billing/transactions` | 账单流水 |
| GET | `/api/v1/billing/subscription` | 我的订阅 |
| POST | `/api/v1/coupons/validate` | 校验优惠券 |
| POST | `/api/v1/admin/orders/:id/refund` | 退款 |
| POST | `/api/v1/admin/payment-channels/:id/test` | 渠道测试 |

### 4.9 支付回调（无会话、无 CSRF）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/payments/notify/:channel` | 异步通知（验签） |
| GET | `/api/v1/payments/return/:channel` | 同步返回落地页 |

### 4.10 站点与设置

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/site` | 站点公开配置（名称、导航、主题 token、开关） |
| GET | `/api/v1/pages/:slug` | 静态页 |
| GET | `/api/v1/admin/settings` | 设置读取（按分组） |
| PATCH | `/api/v1/admin/settings` | 差异保存 |
| POST | `/api/v1/admin/settings/import` | 导入 |
| GET | `/api/v1/admin/settings/export` | 导出 |
| POST | `/api/v1/admin/seo/rebuild-sitemap` | 重建 Sitemap |
| POST | `/api/v1/admin/search/reindex` | 重建索引 |

## 5. 关键接口示例

### 5.1 发主题

```http
POST /api/v1/topics
Content-Type: application/json
X-CSRF-Token: ...
Idempotency-Key: 01J9...

{
  "category_id": "01J8...",
  "title": "如何设计一个可插拔的支付网关？",
  "raw": "## 背景\n...",
  "tags": ["golang", "architecture"],
  "archetype": "question",
  "poll": null
}
```

```json
{
  "data": {
    "id": "01J9...",
    "title": "如何设计一个可插拔的支付网关？",
    "slug": "ruhe-sheji-...",
    "status": "open",
    "url": "/t/01J9...-ruhe-sheji-..."
  },
  "meta": { "request_id": "01J9..." }
}
```

### 5.2 列表（游标）

```http
GET /api/v1/topics?category=tech&sort=latest&limit=20&cursor=01J8...
```

```json
{
  "data": [
    {
      "id": "01J9...",
      "title": "...",
      "excerpt": "...",
      "category": { "id": "01J8...", "name": "技术", "slug": "tech" },
      "author": { "id": "01J7...", "username": "alice", "avatar_url": "..." },
      "tags": ["golang"],
      "reply_count": 12,
      "view_count": 340,
      "last_posted_at": "2026-11-08T09:30:00Z",
      "unread": 3,
      "pinned": false
    }
  ],
  "meta": { "next_cursor": "01J9...", "has_next": true, "request_id": "01J..." }
}
```

### 5.3 错误

```http
POST /api/v1/topics
```

```json
{
  "error": {
    "code": "validation.invalid",
    "message": "提交内容有误",
    "fields": {
      "title": "标题长度需在 5 到 120 个字符之间",
      "tags": "该板块要求至少选择 1 个标签"
    },
    "request_id": "01J..."
  }
}
```

### 5.4 创建订单

```http
POST /api/v1/orders
{
  "product_id": "01J...",
  "quantity": 1,
  "channel_code": "alipay",
  "coupon_code": "WELCOME10"
}
```

```json
{
  "data": {
    "out_trade_no": "GF202611080001",
    "amount_minor": 900,
    "currency": "CNY",
    "status": "pending",
    "expires_at": "2026-11-08T10:30:00Z",
    "payment": {
      "type": "qr",
      "qr_url": "https://...",
      "redirect_url": null
    }
  }
}
```

### 5.5 上传

```http
POST /api/v1/uploads
Content-Type: multipart/form-data

file=@screenshot.png&kind=image
```

```json
{
  "data": {
    "id": "01J...",
    "url": "https://cdn.example.com/uploads/2026/11/01J....webp",
    "thumb_url": "https://cdn.example.com/uploads/2026/11/01J..._thumb.webp",
    "mime": "image/webp",
    "width": 1600,
    "height": 900,
    "size_bytes": 184320
  }
}
```

## 6. SSE 实时通知

```
GET /api/v1/notifications/stream
Accept: text/event-stream
Last-Event-ID: 01J...
```

事件类型：

| event | data | 说明 |
|---|---|---|
| `ready` | `{"user_id":"..."}` | 连接建立 |
| `notification.created` | 通知对象 | 新通知 |
| `unread.count` | `{"count":3}` | 未读总数变化 |
| `order.updated` | `{"out_trade_no":"...","status":"paid"}` | 支付状态（收银台） |
| `heartbeat` | `{}` | 每 25 秒 |

- 断线重连使用 `Last-Event-ID` 补发；服务端保留最近 100 条事件的环形缓冲。
- 不支持 SSE 的客户端降级为 30 秒轮询 `GET /api/v1/notifications?unread=1`。
- 连接数上限按用户与实例配置，超限返回 429。

## 7. 出站 Webhook（P2）

- 事件：`topic.created`、`post.created`、`user.registered`、`order.paid`、`order.refunded`。
- 请求头：`X-GF-Event`、`X-GF-Delivery`、`X-GF-Signature: sha256=HMAC(secret, timestamp + body)`。
- 重试：指数退避，最多 8 次；记录 `webhook_deliveries`。
- 接收方需在 10 秒内返回 2xx。

## 8. 支付回调约定

- 回调路径按渠道注册：`/api/v1/payments/notify/{channel}`。
- 原始请求体必须保留原始字节用于验签，禁止先解析再验签。
- 验签通过后仍需校验：订单存在、渠道一致、金额与币种一致、订单未支付。
- 处理成功返回渠道要求的响应（支付宝 `success`，微信 JSON `{"code":"SUCCESS"}`）。
- 重复回调返回成功但不重复发放；所有回调写入 `payment_notify_logs`。

## 9. 错误码表

| 前缀 | 示例 | 含义 |
|---|---|---|
| `auth.*` | `auth.required` `auth.invalid_token` `auth.2fa_required` | 认证 |
| `permission.*` | `permission.denied` `permission.category_private` | 授权 |
| `validation.*` | `validation.invalid` `validation.too_broad` | 参数 |
| `rate.*` | `rate.limited` | 限流 |
| `user.*` | `user.not_found` `user.suspended` `user.banned` | 用户状态 |
| `topic.*` / `post.*` | `topic.not_found` `topic.closed` `post.hidden` | 内容 |
| `flag.*` / `review.*` | `flag.duplicate` `review.claimed` | 治理 |
| `order.*` / `payment.*` | `order.expired` `payment.amount_mismatch` | 支付 |
| `pagination.*` | `pagination.invalid_cursor` | 分页 |
| `internal.*` | `internal.error` `internal.unavailable` | 服务端 |

约束：错误码稳定，新增只能追加；文案可本地化，代码不变。

## 10. 契约与测试

- 使用 OpenAPI 3.1 描述 `/api/v1`，作为前后端契约与文档来源。
- CI 校验：路由与 OpenAPI 一致、错误码在枚举内、响应字段不缺失。
- 每个写接口至少覆盖：成功、参数错误、未登录、无权限、限流、幂等重复。
- SSE 与回调单独做集成测试（含重放与断线重连）。
