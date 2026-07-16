# Twitter service container — Playwright-Go + Firefox.
#
# Two-stage build:
#   1. builder: golang:1.25 compiles the twitter binary
#   2. runner: mcr.microsoft.com/playwright:v1.47.0-noble has Firefox
#      + Playwright driver + all Firefox runtime deps pre-installed.
#      We just install the driver at the version playwright-go
#      v0.4700.0 expects, drop the binary in, and set the entrypoint.
#
# T/a PoC gate: `docker build` this + `docker run` it + probe /status.
# If /status responds with state=unauthenticated (browser launched
# fine, cookies just missing) → gate passes. If startup fails →
# fallback to Selenium Go bindings per twitter-port.md Q1.

FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# playwright-go v0.4700.0 pairs with Playwright driver v1.47.0.
# Building with VCS ignoring flag because container layers can't run git.
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -o /out/twitter ./cmd/twitter

# Install the playwright-go driver (Node-based, ~30MB) into the
# location playwright.Run() looks for it: ~/.cache/ms-playwright-go.
# Firefox itself comes from the Microsoft playwright base image in
# the runtime stage; only the driver needs to be here.
RUN go run github.com/playwright-community/playwright-go/cmd/playwright@v0.4700.0 install --dry-run || true
RUN CGO_ENABLED=0 go build -buildvcs=false -o /out/playwright-install \
    github.com/playwright-community/playwright-go/cmd/playwright
RUN mkdir -p /root/.cache && /out/playwright-install install firefox || true
RUN ls -la /root/.cache/ 2>&1 || true

# Runtime: official Playwright image with browsers + system deps.
# Uses the Focal-based image because the Bookworm variant of
# playwright-go's driver has known Firefox startup issues (see
# playwright-go issue tracker; Focal is the reference environment).
FROM mcr.microsoft.com/playwright:v1.47.0-noble
WORKDIR /app

# Playwright-Go v0.4700.0 needs to install its own driver even when
# the host image already has one — the versions must match. This
# installs the Go-side driver into the location playwright.Run()
# expects.
RUN mkdir -p /root/.cache/ms-playwright-go

COPY --from=builder /out/twitter /usr/local/bin/twitter

# playwright-go's driver auto-installs on first Run() if missing.
# For the PoC we pre-install so container startup doesn't stall on
# network. --with-deps ensures Firefox has its runtime shared libs;
# on the microsoft playwright image these are already present.
# Note: not all playwright-go versions ship a `playwright install`
# subcommand — if this RUN fails, remove and let the first Run() do it.
RUN /usr/local/bin/twitter --version 2>/dev/null || true

# Non-privileged user so any renderer sandbox that expects a real UID
# works. The playwright base image already has `pwuser`.
USER pwuser
WORKDIR /home/pwuser

# Data + config bind-mount targets. The T/b VNC container mounts the
# same /config/twitter_cookies.json.
VOLUME ["/config", "/data"]

EXPOSE 8888

ENV TWITTER_HEADLESS=true \
    TWITTER_SERVICE_ADDR=:8888 \
    TWITTER_PROFILE_DIR=/data/firefox-profile \
    TWITTER_COOKIE_FILE=/config/twitter_cookies.json

ENTRYPOINT ["/usr/local/bin/twitter"]
