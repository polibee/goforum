const CACHE = "goforum-v2";
const OFFLINE = "/static/offline.html";
self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll([OFFLINE, "/static/dist/app.css"])));
  self.skipWaiting();
});
self.addEventListener("activate", (event) => { event.waitUntil(self.clients.claim()); });
self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET" || req.url.indexOf(self.location.origin) !== 0) return;
  if (req.mode === "navigate") {
    event.respondWith(fetch(req).catch(() => caches.match(req).then((hit) => hit || caches.match(OFFLINE))));
    return;
  }
  if (req.url.indexOf("/static/") !== -1) {
    event.respondWith(caches.match(req).then((hit) => hit || fetch(req).then((res) => {
      const copy = res.clone();
      caches.open(CACHE).then((c) => c.put(req, copy));
      return res;
    })));
  }
});
