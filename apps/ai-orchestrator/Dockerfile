FROM node:20-alpine AS builder
WORKDIR /workspace

RUN npm install -g pnpm@9

COPY pnpm-workspace.yaml package.json pnpm-lock.yaml* ./
COPY tsconfig.base.json ./
COPY packages/audit-client/package.json packages/audit-client/
COPY apps/ai-orchestrator/package.json apps/ai-orchestrator/

RUN pnpm install --frozen-lockfile

COPY packages/audit-client/ packages/audit-client/
COPY apps/ai-orchestrator/ apps/ai-orchestrator/

WORKDIR /workspace/packages/audit-client
RUN pnpm build

WORKDIR /workspace/apps/ai-orchestrator
RUN pnpm build

FROM node:20-alpine
WORKDIR /app
ENV NODE_ENV=production

COPY --from=builder /workspace/apps/ai-orchestrator/dist ./dist
COPY --from=builder /workspace/apps/ai-orchestrator/node_modules ./node_modules
COPY --from=builder /workspace/apps/ai-orchestrator/package.json ./

RUN addgroup -S appgroup && adduser -S appuser -G appgroup
USER appuser

EXPOSE 8084
HEALTHCHECK --interval=10s --timeout=5s --start-period=15s \
  CMD wget -qO- http://localhost:8084/healthz || exit 1

ENTRYPOINT ["node", "dist/index.js"]
