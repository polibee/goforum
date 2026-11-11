# ---- frontend assets -------------------------------------------------------
FROM node:22-alpine AS assets
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --include=dev --no-audit --no-fund
COPY tailwind.config.js ./
COPY web ./web
RUN npm run build

# ---- Go binary -------------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=assets /src/web/static/dist ./web/static/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/goforum ./cmd/goforum

# ---- runtime ---------------------------------------------------------------
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata su-exec \
    && addgroup -S -g 10001 goforum \
    && adduser -S -D -H -u 10001 -G goforum goforum \
    && mkdir -p /app /data/uploads /data/backups \
    && ln -s /data/backups /backups \
    && chown -R goforum:goforum /data

COPY --from=build /out/goforum /app/goforum
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh

ENV GF_ENV_FILE=/data/.env \
    GF_CONFIG=/data/goforum.yaml \
    HOME=/data

WORKDIR /
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/app/goforum"]
