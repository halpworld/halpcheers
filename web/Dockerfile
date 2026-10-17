# Stage 1: Build @halp/core and @halp/app
FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/core/package*.json ./core/
COPY web/app/package*.json ./app/
RUN cd core && npm ci && cd ../app && npm ci
COPY web/core/ ./core/
COPY web/app/ ./app/
RUN cd core && npm run build && cd ../app && npm run build

# Stage 2: Production Caddy edge server with compiled static web assets
FROM caddy:2-alpine
COPY --from=web-builder /src/web/app/index.html /srv/www/index.html
COPY --from=web-builder /src/web/app/landing.html /srv/www/landing.html
COPY --from=web-builder /src/web/app/src/styles.css /srv/www/src/styles.css
COPY --from=web-builder /src/web/app/assets /srv/www/assets
COPY --from=web-builder /src/web/app/dist /srv/www/dist
