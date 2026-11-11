# 10 SEO 设计

> 论坛的核心流量来自搜索。SEO 不是插件，而是渲染、路由、内容状态与后台设置的共同约束。

## 1. 目标与原则

1. **可抓取**：公开内容服务端渲染，爬虫与游客看到同一份 HTML。
2. **可索引**：每个页面类型有明确的 index/noindex 决策，不依赖爬虫猜测。
3. **不重复**：规范 URL、canonical、301 与分页规则消除重复内容。
4. **不泄露**：私有板块、私信、设置、后台永不进入索引与 sitemap。
5. **可度量**：sitemap、结构化数据、404/410、Core Web Vitals 可监控。

禁止：UA 差异化（cloaking）、隐藏文本、伪造结构化数据、把付费墙内容完整输出给爬虫。

## 2. 抓取与状态码

| 场景 | 状态码 | 说明 |
|---|---|---|
| 正常页面 | 200 | 完整 HTML |
| 内容已删除 | 410 | 从索引移除；不返回 200 空页 |
| 内容不存在 | 404 | 真 404，禁止 soft-404 |
| 无权限访问私有内容 | 404 | 不暴露存在性 |
| 旧 slug / 旧路径 | 301 | 永久重定向到规范 URL |
| 临时维护 | 503 + `Retry-After` | 不返回 200 |
| 分页超出上限 | 404 或 200 + noindex | 统一策略，见 6 节 |

- 禁止对已删除内容返回 200 的“内容不存在”页面。
- 404 页面提供搜索框与返回首页，但自身 `noindex`。
- 爬虫与用户看到相同内容；不做基于 UA 的内容裁剪。

## 3. URL 规范

| 类型 | 规范形式 | 示例 |
|---|---|---|
| 主题 | `/t/{ulid}-{slug}` | `/t/01J9A...-ruhe-sheji-zhifu-wangguan` |
| 板块 | `/c/{slug}` | `/c/tech` |
| 标签 | `/tags/{slug}` | `/tags/golang` |
| 用户 | `/u/{username}` | `/u/alice` |
| 静态页 | `/{slug}` | `/privacy` |
| 搜索 | `/search?q=` | 不索引 |
| 分页 | `?page=N` | 主题页/板块页 |

规则：

- 全部小写；单词用连字符；禁止下划线、空格、中文 slug（标题中文时使用拼音或 ID 兜底）。
- 统一 HTTPS、统一主机（`www` 与裸域二选一，另一个 301）。
- 统一尾斜杠策略（默认无尾斜杠，带斜杠 301 去掉）。
- 重复路径 301：仅 ID、仅 slug、大小写变体、多余参数 → 规范 URL。
- slug 变更：旧 slug 写入 `slug_redirects`，永久 301；不删除旧映射。
- 主题 ID 不变；标题修改不改变 ID，slug 可更新并保留旧值映射。
- 追踪参数（`utm_*`、`ref`）不进入 canonical。

## 4. Canonical 规则

| 页面 | canonical |
|---|---|
| 首页 | 站点根 URL |
| 板块/标签/用户 | 自身规范 URL |
| 主题第 1 页 | `/t/{id}-{slug}` |
| 主题第 N 页 | `/t/{id}-{slug}?page=N`（自指，不折叠到第 1 页） |
| 筛选/排序 | 指向无筛选的规范页 |
| 搜索 | 不输出 canonical（noindex） |
| 静态页 | 自身 |
| AMP/打印（如启用） | 指向标准页 |

- canonical 必须是绝对 URL，使用 `seo.canonical_host`。
- 不跨域名 canonical（除非明确的多站点迁移）。
- 分页页自指，避免“所有分页指向第一页”导致后续页不被索引。

## 5. 索引矩阵

| 页面类型 | robots | Sitemap | 备注 |
|---|---|---|---|
| 首页 | `index,follow` | ✅ | — |
| 板块页（有内容） | `index,follow` | ✅ | — |
| 板块页（空） | `noindex,follow` | ❌ | 避免薄内容 |
| 标签页（≥N 主题） | `index,follow` | ✅ | N 可配置，默认 3 |
| 标签页（<N） | `noindex,follow` | ❌ | — |
| 主题详情（公开） | `index,follow` | ✅ | — |
| 主题（待审核/隐藏） | `noindex,noarchive` | ❌ | — |
| 主题（锁定/归档） | `index,follow` | ✅ | 仍有价值 |
| 主题（已删除） | 410 | ❌ | 从 sitemap 移除 |
| 回复锚点页 | `index,follow` | — | canonical 指向主题 |
| 用户主页（公开） | `index,follow` | ✅（可配置） | 隐私用户 noindex |
| 搜索结果 | `noindex,follow` | ❌ | — |
| 筛选/排序组合 | `noindex,follow` | ❌ | 保留爬取路径 |
| 深分页（>N 页） | `noindex,follow` | ❌ | N 默认 50 |
| 通知/私信/设置 | `noindex,nofollow` | ❌ | 需登录 |
| 登录/注册/找回 | `noindex,nofollow` | ❌ | — |
| 后台 `/admin/*` | `noindex,nofollow` | ❌ | robots 也 Disallow |
| 静态页（关于/隐私/条款） | `index,follow` | ✅ | — |
| 404/403/500 | `noindex,nofollow` | ❌ | — |
| 付费内容 | `index,follow` | ✅ | 仅索引公开摘要，见 12 节 |

矩阵是**唯一事实来源**；新增页面类型必须在此登记，并在模板中实现对应 robots 输出。

## 6. 分页策略

- 使用 `?page=N`，页面自指 canonical。
- 输出 `rel="prev"` / `rel="next"`（对爬虫仍有发现价值）。
- 超过最大页数（默认 50）：返回 404 或 200 + `noindex,follow`，二选一并在全站统一。
- 无限滚动：首屏输出前 N 条与分页链接；滚动只做增强，保证无 JS 可翻页。
- 列表排序参数变化视为不同 URL，但统一 `noindex,follow`（避免组合爆炸）。

## 7. Meta 标签

### 7.1 标题模板

```
主题：{topic_title} - {category_name} - {site_name}
板块：{category_name} 板块 - {site_name}
标签：{tag_name} 相关主题 - {site_name}
用户：{username} 的主页 - {site_name}
首页：{site_name} - {site_tagline}
静态页：{page_title} - {site_name}
```

- 后台可改模板；长度建议 ≤ 60 个字符（中文按 2 计），超出截断。
- 主题标题优先保证完整，站点名可截断。

### 7.2 描述

- 自动：取首楼正文纯文本前 150–160 字，去除 Markdown 标记与代码。
- 覆盖：`seo_meta` 支持主题/板块/标签/页面级手动描述。
- 禁止空描述；无内容时回退站点默认描述。

### 7.3 其他头部标签

```html
<link rel="canonical" href="https://example.com/t/01J9...-slug">
<meta name="robots" content="index,follow,max-image-preview:large">
<meta name="theme-color" content="#2563eb">
<link rel="alternate" type="application/rss+xml" title="全站" href="/feed.xml">
<link rel="alternate" type="application/rss+xml" title="板块" href="/c/tech/feed.xml">
```

### 7.4 Open Graph / Twitter

```html
<meta property="og:type" content="article">
<meta property="og:site_name" content="GoForum">
<meta property="og:title" content="如何设计可插拔支付网关">
<meta property="og:description" content="...">
<meta property="og:url" content="https://example.com/t/01J9...-slug">
<meta property="og:image" content="https://example.com/og/t/01J9....png">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta property="article:published_time" content="2026-11-08T10:00:00Z">
<meta property="article:modified_time" content="2026-11-08T12:00:00Z">
<meta property="article:author" content="https://example.com/u/alice">
<meta property="article:section" content="技术">
<meta property="article:tag" content="golang">
<meta name="twitter:card" content="summary_large_image">
```

- `og:image`：实体覆盖 → 默认图 → 自动生成（标题 + 站点名 + 品牌色，1200×630）。
- 自动生成图缓存到 storage，按实体 `updated_at` 失效。
- 主题页 `og:type=article`；板块/首页 `website`；用户 `profile`。

## 8. 结构化数据

统一输出 JSON-LD，放在 `<script type="application/ld+json" nonce>`。

### 8.1 首页：WebSite + SearchAction

```json
{
  "@context": "https://schema.org",
  "@type": "WebSite",
  "name": "GoForum",
  "url": "https://example.com/",
  "potentialAction": {
    "@type": "SearchAction",
    "target": "https://example.com/search?q={search_term_string}",
    "query-input": "required name=search_term_string"
  }
}
```

### 8.2 面包屑

```json
{
  "@context": "https://schema.org",
  "@type": "BreadcrumbList",
  "itemListElement": [
    { "@type": "ListItem", "position": 1, "name": "首页", "item": "https://example.com/" },
    { "@type": "ListItem", "position": 2, "name": "技术", "item": "https://example.com/c/tech" },
    { "@type": "ListItem", "position": 3, "name": "如何设计支付网关" }
  ]
}
```

### 8.3 主题：DiscussionForumPosting

```json
{
  "@context": "https://schema.org",
  "@type": "DiscussionForumPosting",
  "mainEntityOfPage": "https://example.com/t/01J9...-slug",
  "headline": "如何设计可插拔支付网关",
  "datePublished": "2026-11-08T10:00:00Z",
  "dateModified": "2026-11-08T12:00:00Z",
  "author": { "@type": "Person", "name": "alice", "url": "https://example.com/u/alice" },
  "publisher": { "@type": "Organization", "name": "GoForum", "logo": { "@type": "ImageObject", "url": "..." } },
  "image": "https://example.com/og/t/01J9....png",
  "interactionStatistic": [
    { "@type": "InteractionCounter", "interactionType": "https://schema.org/CommentAction", "userInteractionCount": 12 },
    { "@type": "InteractionCounter", "interactionType": "https://schema.org/LikeAction", "userInteractionCount": 34 }
  ],
  "commentCount": 12
}
```

### 8.4 问答：QAPage

```json
{
  "@context": "https://schema.org",
  "@type": "QAPage",
  "mainEntity": {
    "@type": "Question",
    "name": "如何设计可插拔支付网关？",
    "text": "...",
    "answerCount": 3,
    "acceptedAnswer": {
      "@type": "Answer",
      "text": "...",
      "dateCreated": "2026-11-08T11:00:00Z",
      "author": { "@type": "Person", "name": "bob" }
    }
  }
}
```

### 8.5 用户：Person

```json
{
  "@context": "https://schema.org",
  "@type": "Person",
  "name": "alice",
  "url": "https://example.com/u/alice",
  "image": "https://example.com/avatars/01J....webp",
  "description": "后端工程师",
  "memberOf": { "@type": "Organization", "name": "GoForum" }
}
```

### 8.6 付费内容：Product + Offer

```json
{
  "@context": "https://schema.org",
  "@type": "Product",
  "name": "Go 分布式系统实战",
  "description": "...",
  "offers": {
    "@type": "Offer",
    "price": "9.90",
    "priceCurrency": "CNY",
    "availability": "https://schema.org/InStock"
  },
  "isAccessibleForFree": false
}
```

约束：

- 结构化数据必须与页面可见内容一致，禁止虚构评分/作者。
- 多个类型可组合为 `@graph`，但每页不超过必要范围。
- 上线前用 Google Rich Results Test / Schema Validator 校验。

## 9. Sitemap

### 9.1 结构

```
/sitemap.xml                  # 索引
/sitemap-topics-1.xml         # 主题分片
/sitemap-categories.xml       # 板块
/sitemap-tags.xml             # 标签（有内容）
/sitemap-users.xml            # 用户（可配置）
/sitemap-pages.xml            # 静态页
```

### 9.2 规则

- 单文件 ≤ 50,000 URL 且 ≤ 50 MB（未压缩）；超出自动分片。
- 仅包含：公开、未删除、`index` 的页面。
- 排除：私有板块、待审内容、搜索/筛选、通知/私信/设置、后台、空标签、深分页。
- `lastmod` 使用内容 `updated_at`；不要伪造更新时间。
- 支持 gzip；`Content-Type: application/xml`。
- 生成任务：每日一次 + 后台手动触发；写入 `sitemap_state` 记录分片与校验和。
- 删除内容后下一次生成即移除；同时该 URL 返回 410。

### 9.3 索引文件示例

```xml
<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap>
    <loc>https://example.com/sitemap-topics-1.xml</loc>
    <lastmod>2026-11-08T03:00:00Z</lastmod>
  </sitemap>
  <sitemap>
    <loc>https://example.com/sitemap-categories.xml</loc>
    <lastmod>2026-11-08T03:00:00Z</lastmod>
  </sitemap>
</sitemapindex>
```

## 10. robots.txt

按环境动态生成：

```
User-agent: *
Allow: /
Disallow: /admin
Disallow: /api/
Disallow: /search
Disallow: /settings
Disallow: /notifications
Disallow: /messages
Disallow: /install
Disallow: /*?page=*

Sitemap: https://example.com/sitemap.xml
```

- 预发/测试环境输出 `Disallow: /`，避免被收录。
- 后台可编辑，但保存前校验：不能误封 `/`，必须包含 Sitemap。
- 不依赖 robots 做安全控制；私有内容靠权限与 noindex。

## 11. RSS / Atom

| Feed | 路径 | 内容 |
|---|---|---|
| 全站 | `/feed.xml` | 最新公开主题 |
| 板块 | `/c/:slug/feed.xml` | 板块主题 |
| 标签 | `/tags/:slug/feed.xml` | 标签主题 |
| 用户 | `/u/:username/feed.xml` | 用户主题（隐私用户关闭） |

- 默认 20 条，可配置；支持全文或摘要。
- 缓存 5 分钟；支持 ETag/Last-Modified。
- 私有板块与待审内容不进入 Feed。
- Feed 中链接使用绝对 URL。

## 12. 付费内容与 SEO

- 页面对搜索引擎输出公开摘要（标题、前言、目录），不输出付费正文。
- 使用 `isAccessibleForFree: false` 与 `hasPart` 标注付费部分（遵循搜索引擎付费墙规范）。
- 禁止对爬虫输出全文而对用户遮蔽（cloaking）。
- 已购买用户看到全文；未购买用户看到预览与购买入口。
- 付费附件不进入 sitemap。

## 13. 重复内容治理

| 来源 | 处理 |
|---|---|
| 主题仅 ID / 仅 slug / 大小写 | 301 到规范 URL |
| 板块/标签分页与排序组合 | `noindex,follow` |
| 用户主页多 Tab | Tab 使用查询参数；主 Tab 为规范页，其余 noindex |
| 打印页/AMP | canonical 指向标准页 |
| 跨板块移动主题 | 旧 URL 301 到新 URL |
| 主题合并 | 被合并主题 301 到目标主题 |
| 多语言 | `hreflang` + 各自 canonical |

## 14. Core Web Vitals

| 指标 | 目标 | 措施 |
|---|---|---|
| LCP | ≤ 2.5s | 首图预加载、显式尺寸、CDN、AVIF/WebP |
| CLS | ≤ 0.1 | 图片/公告/广告位预留尺寸；骨架屏一致 |
| INP | ≤ 200ms | 减少首屏 JS、按需加载、避免长任务 |

其他：

- 关键 CSS 内联，非关键 CSS 异步。
- 字体 `font-display: swap`，预连接；中文子集可选。
- 游客页缓存 + ETag；登录页不缓存 HTML。
- 不在首屏加载统计/客服等第三方脚本；必须加载时使用 `defer` + CSP nonce。

## 15. 国际化（启用多语言时）

```html
<link rel="alternate" hreflang="zh-CN" href="https://example.com/t/...">
<link rel="alternate" hreflang="en" href="https://example.com/en/t/...">
<link rel="alternate" hreflang="x-default" href="https://example.com/t/...">
```

- 每个语言版本自指 canonical。
- 未翻译内容不输出对应 hreflang。
- 语言切换不自动跳转（避免爬虫困惑），保留显式链接。

## 16. 监控与诊断

- 后台填写 Google / Bing / 百度站点验证码。
- 自动提交 sitemap；记录提交时间与结果。
- 报表：索引量、404 数、410 数、soft-404 数、结构化数据错误、sitemap 抓取错误。
- 站内诊断：
  - 标题重复/过长/为空；
  - 描述缺失；
  - 可索引但未进 sitemap；
  - sitemap 中不可索引；
  - 孤立页面（无内链）；
  - 301 链路过长；
  - canonical 指向自身以外。
- 日志记录爬虫命中（可采样），用于发现抓取异常。

## 17. 后台 SEO 设置页

| 区块 | 内容 |
|---|---|
| 基础 | 站点名、标题模板、默认描述、关键词（可选） |
| 规范 | canonical 主机、尾斜杠策略、www 跳转 |
| 索引 | robots 编辑（校验）、每类页面 noindex 开关 |
| Sitemap | 启用、频率、分片大小、立即重建、最近结果 |
| 社交 | 默认 OG 图、Twitter 卡片类型、自动生成图模板 |
| 验证 | Google/Bing/百度验证码 |
| 分析 | 统计脚本（CSP nonce）、是否匿名化 IP |
| 链接 | 低信任 nofollow、外链跳转页、重定向管理 |
| 诊断 | 上述检查项与修复建议 |

重定向管理：`slug_redirects` 列表，支持新增/删除/导入；禁止循环与链式过长（>3 跳告警）。

## 18. 验收标准

1. 索引矩阵中每个页面类型的 robots 输出与文档一致，自动化测试逐项断言。
2. 每页恰有一个 canonical，且为绝对 URL；分页自指。
3. 已删除内容返回 410 且不在 sitemap；不存在内容返回 404。
4. sitemap 仅含公开、可索引 URL；分片与 lastmod 正确。
5. 主题页 JSON-LD 通过 Rich Results Test；问答页输出 QAPage。
6. 私有板块内容在 HTML、sitemap、RSS、搜索中均不可见。
7. Lighthouse SEO ≥ 100；LCP/CLS/INP 达标。
8. 修改标题模板/默认图/robots 后无需发版即生效。
9. 预发环境默认全站 Disallow。
10. 付费内容不向爬虫输出正文，仅输出摘要与付费标记。
