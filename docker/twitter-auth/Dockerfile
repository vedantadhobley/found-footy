# Raw-Firefox operator-login image for publishing the shared Twitter cookie snapshot.

FROM golang:1.25.11-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG GIT_SHA=unknown
ARG BUILT_AT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -buildvcs=false \
    -ldflags="-s -w -X main.gitSHA=${GIT_SHA} -X main.builtAt=${BUILT_AT}" \
    -o /out/twitter-auth \
    ./cmd/twitter-auth

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        dbus-x11 \
        firefox-esr \
        fluxbox \
        fonts-liberation \
        novnc \
        sqlite3 \
        tini \
        websockify \
        x11vnc \
        xvfb \
    && rm -rf /var/lib/apt/lists/*

RUN adduser --disabled-password --gecos "" --uid 1000 auth \
    && mkdir -p /config /data/firefox-profile \
    && chown -R auth:auth /config /data /home/auth \
    && printf '%s\n' '<html><head><meta http-equiv="refresh" content="0;url=/vnc.html"></head></html>' \
        > /usr/share/novnc/index.html

COPY --from=builder /out/twitter-auth /usr/local/bin/twitter-auth
COPY docker/twitter-auth/entrypoint.sh /usr/local/bin/twitter-auth-entrypoint
RUN chmod +x /usr/local/bin/twitter-auth /usr/local/bin/twitter-auth-entrypoint

USER auth
WORKDIR /home/auth

VOLUME ["/config", "/data"]
EXPOSE 8888 6080

ENV TWITTER_AUTH_ADDR=:8888 \
    TWITTER_AUTH_COOKIE_FILE=/config/twitter_cookies.json \
    TWITTER_AUTH_PROFILE_DIR=/data/firefox-profile \
    TWITTER_AUTH_POLL_INTERVAL=2s

ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/twitter-auth-entrypoint"]
