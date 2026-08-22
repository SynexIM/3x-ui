ARG XRAY_SOURCE=scratch

# ========================================================
# Stage: Frontend (Vite)
# ========================================================
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
COPY internal/web/translation /src/internal/web/translation
RUN npm run build

FROM ${XRAY_SOURCE} AS xray-source

# ========================================================
# Stage: Builder
# ========================================================
FROM golang:1.26-alpine AS builder
WORKDIR /app
ARG TARGETARCH
ARG XRAY_REPO=https://github.com/SynexIM/xray-core.git
ARG XRAY_REF=main

RUN apk --no-cache --update add \
  build-base \
  gcc \
  curl \
  git \
  unzip

COPY . .
COPY --from=frontend /src/internal/web/dist ./internal/web/dist

ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-D_LARGEFILE64_SOURCE"
ENV GOPRIVATE=github.com/SynexIM/*
ENV GOFLAGS=-mod=mod

# The panel and the Xray core both come from private forks, so both steps need
# the same credential; keep them in one RUN so the token never lands in a layer.
#   docker build --secret id=gh_token,env=GH_TOKEN .
RUN --mount=type=secret,id=gh_token \
  --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  --mount=type=bind,from=xray-source,source=/,target=/tmp/xray-source,ro \
  sh -eu -c '\
    if [ -s /run/secrets/gh_token ]; then \
      export GIT_CONFIG_COUNT=1 \
        GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/gh_token)@github.com/.insteadOf" \
        GIT_CONFIG_VALUE_0="https://github.com/"; \
    fi; \
    if [ -f /tmp/xray-source/go.mod ]; then \
      cp -a /tmp/xray-source /tmp/xray-local; \
      go mod edit -replace github.com/xtls/xray-core=/tmp/xray-local; \
      export XRAY_SOURCE_DIR=/tmp/xray-local; \
    fi; \
    go build -ldflags "-w -s -linkmode external -extldflags=-static" -o build/x-ui main.go; \
    XRAY_REPO="$XRAY_REPO" XRAY_REF="$XRAY_REF" ./DockerInit.sh "$TARGETARCH"'

# ========================================================
# Stage: Final Image of 3x-ui
# ========================================================
FROM alpine
ENV TZ=Asia/Tehran
WORKDIR /app

RUN apk add --no-cache --update \
  ca-certificates \
  tzdata \
  fail2ban \
  bash \
  curl \
  openssl

COPY --from=builder /app/build/ /app/
COPY --from=builder /app/DockerEntrypoint.sh /app/
COPY --from=builder /app/x-ui.sh /usr/bin/x-ui
COPY --from=builder /app/internal/web/translation /app/internal/web/translation


# Configure fail2ban
RUN rm -f /etc/fail2ban/jail.d/alpine-ssh.conf \
  && cp /etc/fail2ban/jail.conf /etc/fail2ban/jail.local \
  && sed -i "s/^\[ssh\]$/&\nenabled = false/" /etc/fail2ban/jail.local \
  && sed -i "s/^\[sshd\]$/&\nenabled = false/" /etc/fail2ban/jail.local \
  && sed -i "s/#allowipv6 = auto/allowipv6 = auto/g" /etc/fail2ban/fail2ban.conf

RUN chmod +x \
  /app/DockerEntrypoint.sh \
  /app/x-ui \
  /usr/bin/x-ui

ENV XUI_IN_DOCKER="true"
ENV XUI_MAIN_FOLDER="/app"
ENV XUI_ENABLE_FAIL2BAN="true"
ENV XUI_DB_TYPE=""
ENV XUI_DB_DSN=""
EXPOSE 2053
VOLUME [ "/etc/x-ui" ]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD curl -fsS http://127.0.0.1:2053/ >/dev/null || exit 1
CMD [ "./x-ui" ]
ENTRYPOINT [ "/app/DockerEntrypoint.sh" ]
