# OS of OT — Command Center UI front (optional, isolated, containerized).
#
# Multi-stage build: Node compiles the static bundle, then a minimal
# unprivileged nginx serves it AND reverse-proxies the Stem's API surface
# (/v1, /health, /ws) to the *host* Stem — giving the browser a single
# origin with no CORS anywhere. See ui/nginx/default.conf.template.
#
# The Stem stays headless (#158): this image is a peer surface, never a
# dependency. `docker compose --profile ui up` is the intended entrypoint —
# no local Node/npm required.

# ---- Stage 1: build ui/dist ------------------------------------------------
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json vite.config.ts index.html ./
COPY src ./src
RUN npm run build

# ---- Stage 2: unprivileged static server + reverse proxy -------------------
# nginxinc/nginx-unprivileged runs as non-root (uid 101), listens on 8080,
# and keeps pid/cache under /tmp — which lets the container run with a
# read-only root filesystem (see the `ui` service in docker-compose.yml).
FROM nginxinc/nginx-unprivileged:1.27-alpine

# The entrypoint renders /etc/nginx/templates/*.template with envsubst.
# Restrict substitution to STEM_* so nginx's own $variables are untouched.
ENV NGINX_ENVSUBST_FILTER="^STEM_"
ENV STEM_HOST=host.docker.internal \
    STEM_PORT=8080 \
    STEM_GATEWAY_PORT=9090

COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --chmod=755 nginx/30-check-bind.sh /docker-entrypoint.d/30-check-bind.sh
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 8080
