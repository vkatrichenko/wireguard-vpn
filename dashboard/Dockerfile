# syntax=docker/dockerfile:1.7

# Stage 1 — install production + dev deps from the committed lockfile.
FROM --platform=linux/amd64 node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

# Stage 2 — build the Next.js app and emit standalone output.
FROM --platform=linux/amd64 node:22-alpine AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# Ensure /app/public exists so the runner-stage COPY never fails even if the
# repo's public/ is absent or contains only a .gitkeep that .dockerignore drops.
RUN mkdir -p public && npm run build

# Stage 3 — minimal runtime image.
FROM --platform=linux/amd64 node:22-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
ENV PORT=8080
ENV HOSTNAME=0.0.0.0

RUN addgroup --system --gid 1001 nodejs \
    && adduser --system --uid 1001 nextjs

COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static
COPY --from=builder --chown=nextjs:nodejs /app/public ./public

USER nextjs

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/api/health || exit 1

CMD ["node", "server.js"]
