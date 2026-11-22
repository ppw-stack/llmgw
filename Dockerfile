# syntax=docker/dockerfile:1

# ---- frontend ----
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- build ----
FROM golang:1.27-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/llmgw ./cmd/llmgw

# ---- runtime ----
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S llmgw \
    && adduser -S -G llmgw llmgw \
    && mkdir -p /data \
    && chown llmgw:llmgw /data

COPY --from=build /out/llmgw /usr/local/bin/llmgw

USER llmgw
WORKDIR /data
EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["llmgw", "-healthcheck"]

ENTRYPOINT ["llmgw"]
CMD ["-config", "/etc/llmgw/config.yaml"]
