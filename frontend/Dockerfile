FROM node:22-alpine AS dependencies

WORKDIR /app
RUN corepack enable && corepack prepare pnpm@11.8.0 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

FROM node:22-alpine AS build

WORKDIR /app
RUN corepack enable && corepack prepare pnpm@11.8.0 --activate
COPY --from=dependencies /app/node_modules ./node_modules
COPY . .

ARG NEXT_PUBLIC_PLATFORM_API_URL=/api/platform
ARG NEXT_PUBLIC_PLATFORM_WS_URL=/api/platform/v1/ws
ENV NEXT_PUBLIC_PLATFORM_API_URL=$NEXT_PUBLIC_PLATFORM_API_URL
ENV NEXT_PUBLIC_PLATFORM_WS_URL=$NEXT_PUBLIC_PLATFORM_WS_URL

RUN pnpm build

FROM node:22-alpine AS runtime

ENV NODE_ENV=production
ENV HOSTNAME=0.0.0.0
ENV PORT=3000

WORKDIR /app
RUN addgroup --system --gid 1001 nodejs \
  && adduser --system --uid 1001 nextjs

COPY --from=build --chown=nextjs:nodejs /app/public ./public
COPY --from=build --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=build --chown=nextjs:nodejs /app/.next/static ./.next/static

USER nextjs
EXPOSE 3000

CMD ["node", "server.js"]
