# syntax=docker/dockerfile:1
# Console (Next.js 14) — pnpm + Turborepo monorepo, standalone output.
# BUILD CONTEXT = repo root (needs pnpm-lock.yaml, pnpm-workspace.yaml and the
# workspace dep @he-api/i18n-keys). Mirrors the Go services' root-context pattern.
FROM node:20-alpine AS base
RUN apk add --no-cache libc6-compat && corepack enable
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1

# --- install deps + build the console (turbo builds @he-api/i18n-keys first) ---
FROM base AS build
COPY . .
RUN pnpm install --frozen-lockfile
RUN pnpm exec turbo run build --filter=@he-api/console

# --- minimal runtime: Next standalone server ---
FROM base AS runner
ENV NODE_ENV=production PORT=3000 HOSTNAME=0.0.0.0
# outputFileTracingRoot = repo root → standalone mirrors the monorepo tree.
COPY --from=build /app/apps/console/.next/standalone ./
COPY --from=build /app/apps/console/.next/static ./apps/console/.next/static
COPY --from=build /app/apps/console/public ./apps/console/public
RUN chown -R node:node /app
USER node
EXPOSE 3000
CMD ["node", "apps/console/server.js"]
