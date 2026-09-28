# syntax=docker/dockerfile:1

FROM node:22-alpine AS frontend-build
WORKDIR /build/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/index.html frontend/vite.config.ts frontend/tsconfig*.json ./
COPY frontend/src/ ./src/
RUN npm run build

FROM golang:1.25-alpine AS go-build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -o /out/agent ./cmd/agent \
    && CGO_ENABLED=0 go build -trimpath -o /out/demo-mcp ./cmd/demo-mcp

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /app/data \
    && chown app:app /app/data
WORKDIR /app
COPY --from=go-build /out/agent /usr/local/bin/agent
COPY --from=go-build /out/demo-mcp /usr/local/bin/demo-mcp
COPY --from=frontend-build /build/frontend/dist/ ./frontend/dist/
USER app
CMD ["agent", "--config", "/app/config.yaml"]
