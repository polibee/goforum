import Alpine from "@alpinejs/csp";

// CSP-friendly components: logic lives here, templates only call methods.
Alpine.data("nav", () => ({
  open: false,
  toggle() {
    this.open = !this.open;
  },
  close() {
    this.open = false;
  }
}));

Alpine.data("flash", () => ({
  visible: true,
  dismiss() {
    this.visible = false;
  }
}));

Alpine.data("theme", () => ({
  toggle() {
    const root = document.documentElement;
    const dark = root.classList.toggle("dark");
    try {
      localStorage.setItem("gf-theme", dark ? "dark" : "light");
    } catch (e) {
      /* storage may be unavailable */
    }
  }
}));

function postJSON(url, body) {
  const meta = document.querySelector('meta[name="csrf-token"]');
  return fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      "X-CSRF-Token": meta ? meta.content : ""
    },
    body: JSON.stringify(body)
  });
}

Alpine.data("like", () => ({
  active: false,
  idle: true,
  count: 0,
  busy: false,
  init() {
    this.active = this.$el.dataset.active === "1";
    this.idle = !this.active;
    this.count = Number(this.$el.dataset.count || "0");
  },
  async toggle() {
    if (this.busy) return;
    this.busy = true;
    try {
      const res = await postJSON("/api/v1/reactions", {
        target_type: this.$el.dataset.type,
        target_id: this.$el.dataset.id,
        reaction: "like"
      });
      const payload = await res.json();
      if (!res.ok) throw new Error("failed");
      this.active = payload.data.active;
      this.idle = !this.active;
      this.count = payload.data.count;
      if (this.$el.dataset.reload === "1") window.location.reload();
    } catch (e) {
      /* keep previous state */
    } finally {
      this.busy = false;
    }
  }
}));


Alpine.data("reaction", () => ({
  active: false,
  idle: true,
  count: 0,
  busy: false,
  reaction: "like",
  init() {
    this.reaction = this.$el.dataset.reaction || "like";
    this.active = this.$el.dataset.active === "1";
    this.idle = !this.active;
    this.count = Number(this.$el.dataset.count || "0");
  },
  async toggle() {
    if (this.busy) return;
    this.busy = true;
    try {
      const res = await postJSON("/api/v1/reactions", {
        target_type: this.$el.dataset.type,
        target_id: this.$el.dataset.id,
        reaction: this.reaction
      });
      const payload = await res.json();
      if (!res.ok) throw new Error("failed");
      this.active = payload.data.active;
      this.idle = !this.active;
      this.count = payload.data.count;
      if (this.$el.dataset.reload === "1") {
        location.reload();
        return;
      }
    } catch (e) {
      /* keep previous state */
    } finally {
      this.busy = false;
    }
  }
}));


Alpine.data("bulk", () => ({
  toggleAll(checked) {
    document.querySelectorAll("[data-bulk]").forEach((el) => { el.checked = checked; });
  }
}));

Alpine.data("imageField", () => ({
  uploading: false,
  dragging: false,

  async upload(event) {
    const file = event.target.files && event.target.files[0];
    if (!file) return;
    await this.send(file);
    event.target.value = "";
  },

  onDrop(event) {
    this.dragging = false;
    const file = event.dataTransfer && event.dataTransfer.files && event.dataTransfer.files[0];
    if (file) this.send(file);
  },

  async send(file) {
    if (!file.type.startsWith("image/")) {
      showToast("请选择图片文件");
      return;
    }
    this.uploading = true;
    const body = new FormData();
    body.append("file", file);
    const meta = document.querySelector('meta[name="csrf-token"]');
    try {
      const res = await fetch("/api/v1/uploads", {
        method: "POST",
        body,
        credentials: "same-origin",
        headers: { "X-CSRF-Token": meta ? meta.content : "" }
      });
      const payload = await res.json();
      if (!res.ok || !payload.data || !payload.data.url) throw new Error("upload failed");
      this.$refs.input.value = payload.data.url;
      this.$refs.input.dispatchEvent(new Event("input", { bubbles: true }));
      this.$refs.preview.src = payload.data.url;
      this.$refs.preview.classList.remove("hidden");
      showToast("图片已上传");
    } catch (e) {
      showToast("图片上传失败");
    } finally {
      this.uploading = false;
    }
  },

  clear() {
    if (this.$refs.input) {
      this.$refs.input.value = "";
      this.$refs.input.dispatchEvent(new Event("input", { bubbles: true }));
    }
    if (this.$refs.preview) {
      this.$refs.preview.removeAttribute("src");
      this.$refs.preview.classList.add("hidden");
    }
  },

  sync() {
    const url = this.$refs.input ? this.$refs.input.value.trim() : "";
    const preview = this.$refs.preview;
    if (!preview) return;
    if (!url) {
      preview.classList.add("hidden");
      return;
    }
    preview.src = url;
    preview.classList.remove("hidden");
  },
}));

// Profile form: live counters plus dirty tracking for the floating save bar.
Alpine.data("profileForm", (initial) => ({
  bio: (initial && initial.bio) || "",
  signature: (initial && initial.signature) || "",
  bioMax: 1000,
  sigMax: 300,
  sigLines: 6,
  snapshot: {},
  dirtyCount: 0,

  init() {
    // Snapshot every control so the floating bar only appears after an edit.
    this.$root.querySelectorAll("input[name], textarea[name], select[name]").forEach((el) => {
      if (el.name === "_csrf") return;
      this.snapshot[el.name] = this.value(el);
    });
    this.recount();
  },

  value(el) {
    if (el.type === "checkbox") return el.checked ? "1" : "";
    return el.value;
  },

  watch() {
    this.recount();
  },

  recount() {
    let n = 0;
    this.$root.querySelectorAll("input[name], textarea[name], select[name]").forEach((el) => {
      if (el.name === "_csrf") return;
      const before = this.snapshot[el.name];
      if (before === undefined) return;
      if (this.value(el) !== before) n++;
    });
    this.dirtyCount = n;
  },

  get dirty() { return this.dirtyCount > 0; },

  get bioCount() { return [...this.bio].length; },
  get sigCount() { return [...this.signature].length; },
  get sigLineCount() { return this.signature === "" ? 0 : this.signature.split(String.fromCharCode(10)).length; },

  get bioOver() { return this.bioCount > this.bioMax; },
  get sigOver() { return this.sigCount > this.sigMax || this.sigLineCount > this.sigLines; },

  get canSave() { return !this.bioOver && !this.sigOver; },

  submit() {
    this.$root.submit();
  },
}));

Alpine.data("commandPalette", () => ({
  open: false,
  query: "",
  results: [],
  active: 0,
  loading: false,
  timer: null,
  init() {
    window.addEventListener("keydown", (event) => {
      if ((event.metaKey || event.ctrlKey) && String(event.key).toLowerCase() === "k") {
        event.preventDefault();
        this.show();
      }
    });
  },
  show() {
    this.open = true;
    this.$nextTick(() => setTimeout(() => this.$refs.input && this.$refs.input.focus(), 20));
  },
  close() {
    this.open = false;
    this.query = "";
    this.results = [];
    this.loading = false;
    this.active = 0;
  },
  async search() {
    const q = (this.query || "").trim();
    clearTimeout(this.timer);
    if (!q) {
      this.results = [];
      this.loading = false;
      return;
    }
    this.loading = true;
    this.timer = setTimeout(async () => {
      try {
        const [topicRes, userRes] = await Promise.all([
          fetch("/api/v1/topics?q=" + encodeURIComponent(q) + "&per_page=8", { headers: { Accept: "application/json" } }),
          fetch("/api/v1/users/search?q=" + encodeURIComponent(q), { headers: { Accept: "application/json" } }).catch(() => null),
        ]);
        const payload = await topicRes.json();
        const topics = ((payload.data && payload.data.topics) || []).map((item) => ({ ...item, type: "topic", url: "/t/" + item.number }));
        let users = [];
        if (userRes && userRes.ok) {
          const userPayload = await userRes.json();
          users = ((userPayload.data && userPayload.data.users) || []).map((item) => ({
            ...item, type: "user", title: item.username, url: "/u/" + item.username,
          }));
        }
        this.results = [...users.slice(0, 3), ...topics.slice(0, 5)];
        this.active = 0;
      } catch (e) {
        this.results = [];
      } finally {
        this.loading = false;
      }
    }, 180);
  },
  move(delta) {
    if (this.results.length === 0) return;
    this.active = (this.active + delta + this.results.length) % this.results.length;
  },
  keydown(event) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      this.move(1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      this.move(-1);
    } else if (event.key === "Enter") {
      event.preventDefault();
      this.go(this.active);
    }
  },
  go(index) {
    const item = this.results[index];
    if (item && item.url) {
      window.location.href = item.url;
    }
  }
}));

Alpine.data("searchRecent", () => ({
  items: [],
  init() {
    let list = [];
    try { list = JSON.parse(localStorage.getItem("gf-recent-searches") || "[]"); } catch (e) { list = []; }
    if (!Array.isArray(list)) list = [];
    const q = (this.$el.dataset.query || "").trim();
    if (q) {
      list = [q, ...list.filter((item) => item !== q)].slice(0, 8);
      try { localStorage.setItem("gf-recent-searches", JSON.stringify(list)); } catch (e) { /* ignore */ }
    }
    this.items = list;
  },
  href(item) {
    return "/search?q=" + encodeURIComponent(item);
  },
  clear() {
    this.items = [];
    try { localStorage.removeItem("gf-recent-searches"); } catch (e) { /* ignore */ }
  }
}));

Alpine.data("chatComposer", () => ({
  uploading: false,
  init() {
    this.scrollBottom();
    this.resize();
  },
  scrollBottom() {
    const el = this.$refs.messages;
    if (el) el.scrollTop = el.scrollHeight;
  },
  resize() {
    const el = this.$refs.input;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 160) + "px";
  },
  keydown(event) {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      this.$refs.form.requestSubmit();
    }
  },
  async upload(event) {
    const file = event.target.files && event.target.files[0];
    if (!file) return;
    this.uploading = true;
    const body = new FormData();
    body.append("file", file);
    const meta = document.querySelector('meta[name="csrf-token"]');
    try {
      const res = await fetch("/api/v1/uploads", {
        method: "POST",
        body,
        credentials: "same-origin",
        headers: { "X-CSRF-Token": meta ? meta.content : "" }
      });
      const payload = await res.json();
      if (!res.ok || !payload.data || !payload.data.url) throw new Error("upload failed");
      this.insertImage(`![${file.name}](${payload.data.url})`);
    } catch (e) {
      showToast("图片上传失败");
    } finally {
      this.uploading = false;
      event.target.value = "";
    }
  },
  insertImage(markdown) {
    const el = this.$refs.input;
    if (!el) return;
    const start = el.selectionStart || el.value.length;
    const end = el.selectionEnd || start;
    el.value = el.value.slice(0, start) + markdown + el.value.slice(end);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.focus();
    el.selectionStart = el.selectionEnd = start + markdown.length;
    this.resize();
  }
}));

Alpine.data("thread", () => ({
  toggleAll(open) {
    document.querySelectorAll("details[data-thread]").forEach((d) => { d.open = open; });
  }
}));

Alpine.data("usercard", () => ({
  open: false,
  loaded: false,
  busy: false,
  username: "",
  bio: "",
  trust: 0,
  joined: "",
  topics: 0,
  posts: 0,
  likes: 0,
  following: false,
  init() {
    this.username = this.$el.dataset.username || "";
  },
  async show() {
    this.open = true;
    if (this.loaded || this.busy || !this.username) return;
    this.busy = true;
    try {
      const res = await fetch("/api/v1/users/" + encodeURIComponent(this.username) + "/card", {
        credentials: "same-origin",
        headers: { Accept: "application/json" }
      });
      const payload = await res.json();
      if (!res.ok) throw new Error("failed");
      const d = payload.data || {};
      this.bio = d.bio || "";
      this.trust = d.trust_level || 0;
      this.topics = d.topics || 0;
      this.posts = d.posts || 0;
      this.likes = d.likes || 0;
      this.following = !!d.following;
      if (d.created_at) this.joined = new Date(d.created_at).toLocaleDateString();
      this.loaded = true;
    } catch (e) {
      /* keep placeholder */
    } finally {
      this.busy = false;
    }
  },
  hide() { this.open = false; },
  go() { if (this.username) window.location.href = "/u/" + encodeURIComponent(this.username); }
}));

Alpine.data("bookmark", () => ({
  active: false,
  idle: true,
  count: 0,
  busy: false,
  init() {
    this.active = this.$el.dataset.active === "1";
    this.idle = !this.active;
    this.count = Number(this.$el.dataset.count || "0");
  },
  async toggle() {
    if (this.busy) return;
    this.busy = true;
    try {
      const res = await postJSON("/api/v1/bookmarks", {
        target_type: this.$el.dataset.type,
        target_id: this.$el.dataset.id
      });
      const payload = await res.json();
      if (!res.ok) throw new Error("failed");
      this.active = payload.data.active;
      this.idle = !this.active;
      this.count = payload.data.count;
      if (this.$el.dataset.reload === "1") window.location.reload();
    } catch (e) {
      /* keep previous state */
    } finally {
      this.busy = false;
    }
  }
}));

Alpine.data("follow", () => ({
  active: false,
  idle: true,
  busy: false,
  init() {
    this.active = this.$el.dataset.active === "1";
    this.idle = !this.active;
  },
  async toggle() {
    if (this.busy) return;
    this.busy = true;
    try {
      const res = await postJSON("/api/v1/subscriptions", {
        target_type: this.$el.dataset.type,
        target_id: this.$el.dataset.id
      });
      const payload = await res.json();
      if (!res.ok) throw new Error("failed");
      this.active = payload.data.following;
      this.idle = !this.active;
      if (this.$el.dataset.reload === "1") window.location.reload();
    } catch (e) {
      /* keep previous state */
    } finally {
      this.busy = false;
    }
  }
}));



Alpine.data("editor", () => ({
  sourceEl: null,
  draftKey: "",
  contentVisible: true,
  previewVisible: false,
  splitMode: false,
  loading: false,
  previewHTML: "",
  dialogOpen: false,
  dialogType: "link",
  dialogLink: true,
  dialogImage: false,
  invalidURL: "Please enter a valid http/https URL",
  dialogURL: "",
  dialogText: "",
  dialogAlt: "",
  dialogError: "",
  draftSaved: false,
  saveTimer: null,
  mentionOpen: false,
  mentions: [],
  mentionIndex: 0,
  mentionStart: -1,
  mentionQuery: "",
  mentionTimer: null,
  mentionLoading: false,
  init() {
    this.sourceEl = document.getElementById("raw");
    this.draftKey = this.$el.dataset.draftKey || "gf-draft";
    this.invalidURL = this.$el.dataset.invalidUrl || this.invalidURL;
    if (!this.sourceEl) return;
    try {
      const saved = localStorage.getItem(this.draftKey);
      if (saved && !this.sourceEl.value) this.sourceEl.value = saved;
    } catch (e) { /* storage may be unavailable */ }
    this.sourceEl.addEventListener("input", () => {
      this.saveDraft();
      this.updateMention();
    });
    this.sourceEl.addEventListener("keydown", (e) => this.mentionKeydown(e));
    this.sourceEl.addEventListener("blur", () => setTimeout(() => { this.mentionOpen = false; }, 150));
  },
  updateMention() {
    const ta = this.textarea();
    if (!ta) return;
    const before = ta.value.slice(0, ta.selectionStart);
    const at = before.lastIndexOf("@");
    if (at < 0) { this.mentionOpen = false; return; }
    const fragment = before.slice(at + 1);
    if (!/^[A-Za-z0-9_一-龥]{1,30}$/.test(fragment)) { this.mentionOpen = false; return; }
    const prev = at > 0 ? before.charAt(at - 1) : "";
    if (prev && prev.trim() !== "" && !"(（[【,，".includes(prev)) { this.mentionOpen = false; return; }
    this.mentionQuery = fragment;
    this.mentionStart = at;
    this.mentionOpen = true;
    this.mentionLoading = true;
    clearTimeout(this.mentionTimer);
    const query = fragment;
    this.mentionTimer = setTimeout(async () => {
      if (query !== this.mentionQuery) return;
      try {
        const res = await fetch("/api/v1/users/search?q=" + encodeURIComponent(query), { headers: { Accept: "application/json" } });
        const payload = await res.json();
        if (query !== this.mentionQuery) return;
        this.mentions = (payload.data && payload.data.users) || [];
        this.mentionIndex = 0;
      } catch (e) {
        this.mentions = [];
      } finally {
        this.mentionLoading = false;
      }
    }, 160);
  },
  mentionKeydown(e) {
    if (!this.mentionOpen) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      if (this.mentions.length) this.mentionIndex = (this.mentionIndex + 1) % this.mentions.length;
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      if (this.mentions.length) this.mentionIndex = (this.mentionIndex - 1 + this.mentions.length) % this.mentions.length;
    } else if (e.key === "Enter" || e.key === "Tab") {
      if (this.mentions.length) {
        e.preventDefault();
        this.selectMention(this.mentionIndex);
      }
    } else if (e.key === "Escape") {
      this.mentionOpen = false;
    }
  },
  selectMention(index) {
    const mention = this.mentions[index];
    const ta = this.textarea();
    if (!mention || !ta || this.mentionStart < 0) return;
    const insert = "@" + mention.username + " ";
    const after = ta.value.slice(ta.selectionStart);
    ta.value = ta.value.slice(0, this.mentionStart) + insert + after;
    const caret = this.mentionStart + insert.length;
    this.mentionStart = -1;
    this.mentionOpen = false;
    this.mentions = [];
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    ta.focus();
    ta.selectionStart = ta.selectionEnd = caret;
  },
  textarea() { return this.sourceEl || document.getElementById("raw"); },
  saveDraft() {
    const ta = this.textarea();
    if (!ta) return;
    clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      try {
        localStorage.setItem(this.draftKey, ta.value);
        this.draftSaved = true;
      } catch (e) { /* ignore */ }
    }, 600);
  },
  clearDraft() {
    try { localStorage.removeItem(this.draftKey); } catch (e) {}
    const ta = this.textarea();
    if (ta) { ta.value = ""; ta.focus(); }
    this.draftSaved = false;
  },
  wrap(before, after, placeholder) {
    const ta = this.textarea();
    if (!ta) return;
    const start = ta.selectionStart;
    const end = ta.selectionEnd;
    const selected = ta.value.slice(start, end) || placeholder;
    ta.value = ta.value.slice(0, start) + before + selected + after + ta.value.slice(end);
    ta.dispatchEvent(new Event("input"));
    ta.focus();
    ta.selectionStart = start + before.length;
    ta.selectionEnd = start + before.length + selected.length;
  },
  prefix(prefix) {
    const ta = this.textarea();
    if (!ta) return;
    const start = ta.selectionStart;
    const lineStart = ta.value.lastIndexOf("\n", start - 1) + 1;
    ta.value = ta.value.slice(0, lineStart) + prefix + ta.value.slice(lineStart);
    ta.dispatchEvent(new Event("input"));
    ta.focus();
    ta.selectionStart = ta.selectionEnd = start + prefix.length;
  },
  bold() { this.wrap("**", "**", "bold"); },
  italic() { this.wrap("*", "*", "italic"); },
  strike() { this.wrap("~~", "~~", "strike"); },
  code() { this.wrap("`", "`", "code"); },
  heading() { this.prefix("## "); },
  quote() { this.prefix("> "); },
  bullet() { this.prefix("- "); },
  ordered() { this.prefix("1. "); },
  openLink() {
    const ta = this.textarea();
    const selected = ta ? ta.value.slice(ta.selectionStart, ta.selectionEnd) : "";
    this.dialogType = "link";
    this.dialogLink = true;
    this.dialogImage = false;
    this.dialogURL = "https://";
    this.dialogText = selected;
    this.dialogError = "";
    this.dialogOpen = true;
    this.focusDialog();
  },
  openImage() {
    this.dialogType = "image";
    this.dialogLink = false;
    this.dialogImage = true;
    this.dialogURL = "https://";
    this.dialogAlt = "";
    this.dialogError = "";
    this.dialogOpen = true;
    this.focusDialog();
  },
  focusDialog() {
    this.$nextTick(() => {
      setTimeout(() => {
        const el = this.$refs.dialogUrl;
        if (el) {
          el.focus();
          el.select();
        }
      }, 30);
    });
  },
  trapFocus(event) {
    const root = this.$refs.dialogPanel;
    if (!root) return;
    const nodes = [...root.querySelectorAll("button, input, textarea, select, a[href]")].filter((el) => !el.disabled && el.offsetParent !== null);
    if (nodes.length === 0) return;
    const first = nodes[0];
    const last = nodes[nodes.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  },
  closeDialog() {
    this.dialogOpen = false;
    this.dialogError = "";
    const ta = this.textarea();
    if (ta) ta.focus();
  },
  applyDialog() {
    const url = (this.dialogURL || "").trim();
    if (!/^(https?:\/\/|\/)/i.test(url)) {
      this.dialogError = this.invalidURL;
      return;
    }
    const ta = this.textarea();
    if (!ta) return;
    const start = ta.selectionStart;
    const end = ta.selectionEnd;
    let markdown = "";
    if (this.dialogType === "image") {
      markdown = "![" + (this.dialogAlt || "").trim() + "](" + url + ")";
    } else {
      const label = (this.dialogText || "").trim() || url;
      markdown = "[" + label + "](" + url + ")";
    }
    ta.value = ta.value.slice(0, start) + markdown + ta.value.slice(end);
    ta.dispatchEvent(new Event("input"));
    ta.selectionStart = ta.selectionEnd = start + markdown.length;
    this.dialogOpen = false;
    ta.focus();
  },
  panels() { return document.getElementById("editor-panels"); },
  showContent() {
    this.contentVisible = true; this.previewVisible = false; this.splitMode = false;
    const p = this.panels(); if (p) p.classList.remove("lg:grid-cols-2");
  },
  showPreview() {
    this.contentVisible = false; this.previewVisible = true; this.splitMode = false;
    const p = this.panels(); if (p) p.classList.remove("lg:grid-cols-2");
    this.render();
  },
  showSplit() {
    this.contentVisible = true; this.previewVisible = true; this.splitMode = true;
    const p = this.panels(); if (p) p.classList.add("lg:grid-cols-2");
    this.render();
  },
  async render() {
    const ta = this.textarea();
    if (!ta) return;
    this.loading = true;
    try {
      const res = await postJSON("/api/v1/markdown/preview", { raw: ta.value });
      const payload = await res.json();
      if (!res.ok) throw new Error("failed");
      this.previewHTML = payload.data.html || "";
    } catch (e) {
      this.previewHTML = '<p class="text-sm text-danger">Preview failed. Please retry.</p>';
    } finally {
      const box = document.getElementById("preview");
      if (box) box.innerHTML = this.previewHTML;
      this.loading = false;
    }
  },
  undo() {
    const ta = this.textarea();
    if (ta) { ta.focus(); document.execCommand("undo"); }
  },
  redo() {
    const ta = this.textarea();
    if (ta) { ta.focus(); document.execCommand("redo"); }
  }
}));

function openLightbox(src, alt) {
  const old = document.getElementById("gf-lightbox");
  if (old) old.remove();
  const box = document.createElement("div");
  box.id = "gf-lightbox";
  box.className = "fixed inset-0 z-[100] flex items-center justify-center bg-black/80 p-4";
  const img = document.createElement("img");
  img.src = src;
  img.alt = alt || "";
  img.className = "max-h-full max-w-full rounded object-contain shadow-2xl";
  const close = document.createElement("button");
  close.type = "button";
  close.className = "absolute right-4 top-4 flex h-9 w-9 items-center justify-center rounded-full bg-white/15 text-xl text-white hover:bg-white/25";
  close.setAttribute("aria-label", "Close");
  close.textContent = "×";
  const dismiss = () => { box.remove(); document.body.style.overflow = ""; };
  close.addEventListener("click", dismiss);
  box.addEventListener("click", (e) => { if (e.target === box) dismiss(); });
  box.appendChild(img);
  box.appendChild(close);
  document.body.appendChild(box);
  document.body.style.overflow = "hidden";
}

document.addEventListener("click", (e) => {
  const img = e.target.closest && e.target.closest(".gf-prose img");
  if (!img || !img.src) return;
  e.preventDefault();
  openLightbox(img.src, img.alt);
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    const box = document.getElementById("gf-lightbox");
    if (box) { box.remove(); document.body.style.overflow = ""; }
  }
});


Alpine.data("share", () => ({
  async run() {
    const url = window.location.href.split("#")[0];
    const title = (this.$el && this.$el.dataset && this.$el.dataset.title) || document.title;
    try {
      if (navigator.share) {
        await navigator.share({ title, url });
        return;
      }
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(url);
      } else {
        const input = document.createElement("input");
        input.value = url;
        input.style.position = "fixed";
        input.style.opacity = "0";
        document.body.appendChild(input);
        input.select();
        document.execCommand("copy");
        input.remove();
      }
      const en = (document.documentElement.lang || "").startsWith("en");
      showToast(en ? "Link copied" : "链接已复制");
    } catch (e) {
      if (e && e.name === "AbortError") return;
      const en = (document.documentElement.lang || "").startsWith("en");
      showToast(en ? "Copy failed. Please copy the address bar link." : "复制失败，请手动复制地址栏链接");
    }
  },
}));

Alpine.data("copyText", () => ({
  done: false,
  async copy(text, doneLabel) {
    try {
      await navigator.clipboard.writeText(text);
    } catch (e) {
      const area = document.createElement("textarea");
      area.value = text;
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      document.execCommand("copy");
      document.body.removeChild(area);
    }
    this.done = true;
    showToast(doneLabel || "已复制");
    setTimeout(() => (this.done = false), 1800);
  },
}))

Alpine.data("adEditor", () => ({
  format: "text",
  init() { this.format = this.$el.dataset.format || "text"; },
}));

Alpine.data("adPurchase", () => ({
  slot: "sidebar",
  months: 1,
  format: "text",
  prices: {},
  sizes: {},
  init() {
    try { this.prices = JSON.parse(this.$el.dataset.prices || "{}"); } catch (e) { this.prices = {}; }
    try { this.sizes = JSON.parse(this.$el.dataset.sizes || "{}"); } catch (e) { this.sizes = {}; }
    const firstPaid = Object.keys(this.prices).find((key) => key.endsWith(".text") && Number(this.prices[key]) > 0);
    if (firstPaid) this.slot = firstPaid.slice(0, -5);
  },
  total() {
    return (Number(this.prices[this.slot + "." + this.format]) || 0) * (Number(this.months) || 0);
  },
  totalLabel() {
    return "$" + (this.total() / 100).toFixed(2);
  },
  recommendedSize() {
    return this.sizes[this.slot] || "300 × 250";
  },
}));

Alpine.data("sidebarWidget", () => ({
  type: "links",
  rows: [],
  init() {
    this.type = this.$el.dataset.type || "links";
    try {
      const parsed = JSON.parse(this.$el.dataset.links || "[]");
      if (Array.isArray(parsed)) {
        this.rows = parsed.map((row) => ({ label: row.label || "", url: row.url || "" }));
      }
    } catch (e) { this.rows = []; }
    if (!this.rows.length) this.rows = [{ label: "", url: "" }];
  },
  add() { this.rows.push({ label: "", url: "" }); },
  remove(index) {
    this.rows.splice(index, 1);
    if (!this.rows.length) this.rows.push({ label: "", url: "" });
  },
}));

Alpine.store("gfConfirm", {
  open: false,
  message: "",
  form: null,
  ask(form, message) {
    this.form = form;
    this.message = message || "";
    this.open = true;
    setTimeout(() => { const el = document.getElementById("gf-confirm-cancel"); if (el) el.focus(); }, 40);
  },
  accept() {
    const form = this.form;
    this.cancel();
    if (form) {
      form.dataset.confirmed = "1";
      form.submit();
    }
  },
  cancel() {
    this.open = false;
    this.form = null;
    this.message = "";
  }
});

document.addEventListener("submit", (event) => {
  const form = event.target.closest && event.target.closest("form[data-confirm]");
  if (!form || form.dataset.confirmed === "1") return;
  event.preventDefault();
  Alpine.store("gfConfirm").ask(form, form.dataset.confirm);
});

document.addEventListener("click", (event) => {
  const trigger = event.target.closest && event.target.closest("[data-details-open]");
  if (!trigger) return;
  const details = document.getElementById(trigger.dataset.detailsOpen);
  if (details) {
    details.open = true;
    details.scrollIntoView({ behavior: "smooth", block: "start" });
  }
});

// Menus built on <details> stay open until their summary is clicked again,
// which feels broken. Close any open menu when the user clicks elsewhere,
// presses Escape, or clicks a link inside it.
document.addEventListener("click", (event) => {
  const openMenus = document.querySelectorAll("details[data-menu][open]");
  if (!openMenus.length) return;

  const inside = event.target.closest && event.target.closest("details[data-menu]");
  // A click inside a menu closes it only when it was a navigation link; clicks
  // on buttons (theme toggle) keep the menu open so the UI does not flicker.
  if (inside) {
    if (event.target.closest("a")) inside.open = false;
    return;
  }
  openMenus.forEach((d) => (d.open = false));
});

document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  document.querySelectorAll("details[data-menu][open]").forEach((d) => {
    d.open = false;
    const summary = d.querySelector("summary");
    if (summary) summary.focus();
  });
});

Alpine.data("widgetPresets", () => ({
  open: false,
  openPresets() {
    this.open = true;
    document.body.style.overflow = "hidden";
  },
  closePresets() {
    this.open = false;
    document.body.style.overflow = "";
  },
}));

Alpine.data("settingsPage", () => ({
  original: {},
  dirty: {},
  dirtyCount: 0,
  activeFilter: "",
  noMatch: false,
  current: "",

  init() {
    // Snapshot every control so reset/compare works without server round-trips.
    this.$root.querySelectorAll("[data-setting-row]").forEach((row) => {
      const field = row.querySelector("[name^='setting.']");
      if (!field) return;
      const key = field.name.replace("setting.", "");
      this.original[key] = this.readValue(field);
    });
    this.watchGroups();
  },

  readValue(field) {
    if (field.type === "checkbox") {
      return field.checked ? "true" : "false";
    }
    return field.value;
  },

  mark(key, value, defaultText) {
    const before = this.original[key];
    if (before === undefined) return;
    const changed = String(value) !== String(before);
    if (changed) {
      this.dirty[key] = true;
    } else {
      delete this.dirty[key];
    }
    this.dirtyCount = Object.keys(this.dirty).length;
  },

  isDirty(key) {
    return !!this.dirty[key];
  },

  // Restore a single field to the definition default.
  resetField(key, defaultText, type) {
    const row = [...this.$root.querySelectorAll("[data-setting-row]")]
      .find((r) => r.querySelector(`[name='setting.${key}']`));
    if (!row) return;
    const field = row.querySelector(`[name='setting.${key}']`);
    if (!field) return;

    if (type === "bool") {
      let target = defaultText === "true";
      // A checkbox is paired with a hidden false input; set both consistently.
      row.querySelectorAll(`input[name='setting.${key}']`).forEach((el) => {
        if (el.type === "checkbox") el.checked = target;
      });
      this.mark(key, target ? "true" : "false", defaultText);
      return;
    }
    field.value = defaultText;
    // Keep nested Alpine state (e.g. the colour swatch) in sync. Dispatching
    // input re-runs the row's mark() with the fresh value.
    if (field.hasAttribute("x-model")) {
      field.dispatchEvent(new Event("input", { bubbles: true }));
      field.value = defaultText;
    }
    this.mark(key, defaultText, defaultText);
  },

  // Discard local edits on one field and return to the value it had on load.
  revertField(key, type) {
    const row = [...this.$root.querySelectorAll("[data-setting-row]")]
      .find((r) => r.querySelector(`[name='setting.${key}']`));
    if (!row) return;
    const original = this.original[key];
    if (original === undefined) return;

    if (type === "bool") {
      row.querySelectorAll(`input[name='setting.${key}']`).forEach((el) => {
        if (el.type === "checkbox") el.checked = original === "true";
      });
    } else {
      const field = row.querySelector(`[name='setting.${key}']`);
      if (!field) return;
      field.value = original;
      if (field.hasAttribute("x-model")) {
        field.dispatchEvent(new Event("input", { bubbles: true }));
        field.value = original;
      }
    }
    delete this.dirty[key];
    this.dirtyCount = Object.keys(this.dirty).length;
  },

  toggleSecret(ref) {
    const field = this.$refs[ref];
    if (!field) return;
    field.type = field.type === "password" ? "text" : "password";
  },

  filter(query) {
    const q = (query || "").trim().toLowerCase();
    this.activeFilter = q;
    let anyVisible = false;

    this.$root.querySelectorAll("[data-settings-group]").forEach((group) => {
      const rows = [...group.querySelectorAll("[data-setting-row]")];
      let visible = 0;
      rows.forEach((row) => {
        const haystack = (row.dataset.search || "").toLowerCase();
        const show = q === "" || haystack.includes(q);
        row.classList.toggle("hidden", !show);
        if (show) visible++;
      });
      const hideGroup = rows.length > 0 && visible === 0;
      group.classList.toggle("hidden", hideGroup);
      if (!hideGroup && visible > 0) anyVisible = true;
      const nav = this.$root.querySelector(`[data-nav-group="${group.id}"]`);
      if (nav) nav.classList.toggle("hidden", hideGroup);
    });

    this.noMatch = q !== "" && !anyVisible;
  },

  // Follow scrolling and highlight the group currently in view.
  watchGroups() {
    if (!("IntersectionObserver" in window)) return;
    const groups = [...this.$root.querySelectorAll("[data-settings-group]")];
    if (!groups.length) return;
    const observer = new IntersectionObserver(
      (entries) => {
        const shown = entries
          .filter((e) => e.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        if (shown.length) this.current = shown[0].target.id;
      },
      { rootMargin: "-96px 0px -60% 0px", threshold: 0 },
    );
    groups.forEach((g) => observer.observe(g));
  },

  jump(id) {
    const target = document.getElementById(id);
    if (!target) return;
    this.current = id;
    const top = target.getBoundingClientRect().top + window.scrollY - 120;
    window.scrollTo({ top, behavior: "smooth" });
  },
}));

Alpine.data("billingPanel", () => ({
  tab: "products",
  init() {
    // ?tab=channels lets the token form return to the channel tab after save.
    const requested = this.$root.dataset.openTab;
    if (requested) this.tab = requested;
  },
}));

// Token picker for the Deipay payment channel. Selection is a set of catalog
// IDs; the server converts them into the stored comma-separated setting.
Alpine.data("tokenPicker", (selected) => ({
  chosen: Array.isArray(selected) ? selected.slice() : [],
  query: "",
  total: 0,
  // Deipay invoices carry a single chainId, so a selection must stay on one
  // network. Picking a token on another chain swaps the whole selection.
  chainWarn: "",

  init() {
    this.total = this.$root.querySelectorAll("[data-token-option]").length;
  },

  has(id) {
    return this.chosen.includes(id);
  },

  chainOf(id) {
    const el = this.$root.querySelector(`[data-token-option="${id}"]`);
    return el ? el.dataset.tokenChainId || "" : "";
  },

  toggle(id) {
    const i = this.chosen.indexOf(id);
    if (i >= 0) {
      this.chosen.splice(i, 1);
      this.chainWarn = "";
      return;
    }
    const chain = this.chainOf(id);
    const otherChain = this.chosen.find((existing) => {
      const c = this.chainOf(existing);
      return c && chain && c !== chain;
    });
    if (otherChain) {
      const label = this.chainLabel(id);
      this.chosen = [id];
      this.chainWarn = `已切换为 ${label}：一张账单只能使用一个网络，其他网络的币种已取消。`;
      return;
    }
    this.chosen.push(id);
    this.chainWarn = "";
  },

  chainLabel(id) {
    const el = this.$root.querySelector(`[data-token-option="${id}"]`);
    return el ? el.dataset.tokenChain || "" : "";
  },

  clearWarn() {
    this.chainWarn = "";
  },

  matches(label) {
    const q = this.query.trim().toLowerCase();
    return q === "" || (label || "").toLowerCase().includes(q);
  },

  selectPopular() {
    this.chosen = [...this.$root.querySelectorAll("[data-token-option][data-popular='1']")]
      .map((el) => el.dataset.tokenOption);
  },

  clearAll() {
    this.chosen = [];
  },

  get count() {
    return this.chosen.length;
  },

  get summary() {
    if (this.chosen.length === 0) return "";
    // Include the chain so USDT on Ethereum and USDT on Polygon stay distinct.
    const labels = this.chosen.map((id) => {
      const el = this.$root.querySelector(`[data-token-option="${id}"]`);
      if (!el) return id;
      const chain = el.dataset.tokenChain || "";
      return chain ? `${el.dataset.tokenSymbol}(${chain})` : el.dataset.tokenSymbol;
    });
    return labels.join(" · ");
  },
}));

Alpine.data("opsPanel", () => ({
  tab: "rules",
}));

Alpine.data("adminMenu", () => ({
  open: false,
  toggle() { this.open = !this.open; },
  close() { this.open = false; },
}));

Alpine.data("backToTop", () => ({
  visible: false,
  init() {
    const update = () => { this.visible = window.scrollY > 600; };
    window.addEventListener("scroll", update, { passive: true });
    update();
  },
  go() {
    window.scrollTo({ top: 0, behavior: "smooth" });
  },
}));

document.addEventListener("click", (e) => {
  const link = e.target.closest && e.target.closest("[data-quote-post]");
  if (!link) return;
  const form = document.getElementById("reply");
  if (!form) return;
  const ta = form.querySelector('textarea[name="raw"]');
  const replyTo = form.querySelector('input[name="reply_to_post_id"]');
  if (!ta || !replyTo) return;
  e.preventDefault();
  const id = link.dataset.quotePost;
  const author = link.dataset.quoteAuthor || "";
  const nl = String.fromCharCode(10);
  const lines = String(link.dataset.quoteRaw || "").trim().split(nl);
  let quote = author ? "> @" + author + nl : "> ";
  quote += lines.map((line) => (line ? "> " + line : ">")).join(nl) + nl + nl;
  if (replyTo.value !== id) {
    replyTo.value = id;
    ta.value = quote + ta.value;
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  }
  form.scrollIntoView({ behavior: "smooth", block: "start" });
  setTimeout(() => {
    ta.focus();
    ta.selectionStart = ta.selectionEnd = ta.value.length;
  }, 300);
});

document.addEventListener("click", async (e) => {
  const link = e.target.closest && e.target.closest("[data-floor-link]");
  if (!link) return;
  const id = link.dataset.floorLink;
  if (!id) return;
  e.preventDefault();
  const post = document.getElementById("post-" + id);
  const url = window.location.origin + window.location.pathname + "#post-" + id;
  const en = (document.documentElement.lang || "").startsWith("en");
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(url);
    } else {
      const input = document.createElement("input");
      input.value = url;
      input.style.position = "fixed";
      input.style.opacity = "0";
      document.body.appendChild(input);
      input.select();
      document.execCommand("copy");
      input.remove();
    }
    showToast(en ? "Floor link copied" : "楼层链接已复制");
  } catch (err) {
    showToast(en ? "Copy failed" : "复制失败，请手动复制地址栏链接");
  }
  if (post) {
    document.querySelectorAll(".gf-post-flash").forEach((el) => el.classList.remove("gf-post-flash"));
    post.classList.add("gf-post-flash");
    setTimeout(() => post.classList.remove("gf-post-flash"), 2600);
    history.replaceState(null, "", "#post-" + id);
    post.scrollIntoView({ behavior: "smooth", block: "start" });
  }
});

function showToast(text, url) {
  const el = document.createElement("a");
  el.href = url || "/notifications";
  el.className = "gf-toast";
  el.setAttribute("role", "status");
  el.textContent = text;
  document.body.appendChild(el);
  requestAnimationFrame(() => el.classList.add("gf-toast-show"));
  setTimeout(() => {
    el.classList.remove("gf-toast-show");
    setTimeout(() => el.remove(), 300);
  }, 6000);
}


if (window.EventSource && document.body.dataset.auth === "1") {
  const stream = new EventSource("/api/v1/notifications/stream");
  let lastUnread = null;
  stream.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      const count = Number(data.unread || 0);
      document.querySelectorAll("[data-notification-unread]").forEach((el) => {
        el.textContent = String(count);
        el.classList.toggle("hidden", count <= 0);
      });
      if (lastUnread !== null && count > lastUnread && data.latest) {
        const isEN = (document.documentElement.lang || "").startsWith("en");
        const labels = isEN
          ? { "post.reply": "Reply", "post.quote": "Quote", "post.watch": "Watched topic", "post.mention": "Mention", reaction: "Like", "message.new": "Message" }
          : { "post.reply": "回复", "post.quote": "引用回复", "post.watch": "关注主题", "post.mention": "@提及", reaction: "点赞", "message.new": "私信" };
        const title = data.latest.Title ? " · " + data.latest.Title : "";
        showToast((labels[data.latest.Type] || (isEN ? "Notification" : "通知")) + title, data.latest.URL);
      }
      lastUnread = count;
    } catch (e) {
      /* ignore malformed events */
    }
  };
}

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => { navigator.serviceWorker.register("/static/sw.js").catch(() => {}); });
}

window.Alpine = Alpine;
Alpine.start();
