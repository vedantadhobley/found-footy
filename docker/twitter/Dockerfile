# Twitter service container — Playwright-Go + Firefox, optionally
# wrapped in a VNC stack (Xvfb + fluxbox + x11vnc + websockify+noVNC)
# for the twitter-vnc container used during manual re-auth.
#
# ONE image shape covers BOTH headless and VNC containers. The VNC
# extras are installed via a WITH_VNC=true build arg. The runtime
# entrypoint conditionally boots the VNC daemons based on the
# TWITTER_VNC_MODE=true env var (compose sets it per service).
#
# Rationale for one-image-two-modes (matches Python's shape per
# decisions.md 2026-07-22 — reconciliation): headless + VNC scrapers
# should run the SAME code; the only difference is one has a visible
# display for humans to observe/interact with. Divergent images would
# risk scraping-behavior drift.
#
# Build examples:
#   docker build -f docker/twitter/Dockerfile -t found-footy-twitter .
#   docker build -f docker/twitter/Dockerfile \
#       --build-arg WITH_VNC=true \
#       -t found-footy-twitter-vnc .

# ────── build stage ──────
# Same as top-level Dockerfile's build stage — parameterizable on
# BINARY so the twitter-vnc build path can reuse the same builder
# even though only BINARY=twitter makes sense here.
FROM golang:1.25-bookworm AS builder

ARG BINARY=twitter
WORKDIR /src

# Cache modules separately from source so a .go edit doesn't
# re-download.
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# §11 deploy tracking — bake identity into the binary.
ARG GIT_SHA=unknown
ARG BUILT_AT=unknown

# CGO disabled — pure Go binary, portable across the runtime image.
# -buildvcs=false because container layers can't run git (parent .git
# dir isn't in the build context and git errors otherwise).
RUN CGO_ENABLED=0 GOOS=linux go build \
    -buildvcs=false \
    -ldflags="-s -w -X main.gitSHA=${GIT_SHA} -X main.builtAt=${BUILT_AT}" \
    -o /out/twitter \
    ./cmd/${BINARY}

# Playwright-go driver install. playwright-go v0.6100.0 pairs with
# the Playwright JS driver v1.61.1 (matches the runtime base image
# tag exactly). Skipping this = playwright.Run() at runtime tries to
# download the driver on first launch, which stalls container startup
# on network + fails offline. Install here, copy to runtime below.
RUN CGO_ENABLED=0 go build -buildvcs=false \
    -o /out/playwright-install \
    github.com/mxschmitt/playwright-go/cmd/playwright
RUN mkdir -p /root/.cache && /out/playwright-install install firefox

# ────── runtime stage ──────
# Microsoft's Playwright image ships Firefox + all Playwright system
# libs (fontconfig, dbus, freetype, gtk, etc.) matched to driver
# v1.61.1. Base for both headless and VNC containers — VNC extras
# added conditionally below.
FROM mcr.microsoft.com/playwright:v1.61.1-noble

# VNC extras — only installed for the twitter-vnc build. Adds ~150MB
# (fluxbox, x11vnc, novnc, websockify + their transitive deps); gated
# so the headless twitter image stays lean.
#
# Xvfb is intentionally kept HERE (in the WITH_VNC=true block) and
# NOT in a base layer: the headless container runs Playwright with
# headless=true which uses Playwright's built-in headless mode, no
# Xvfb needed. Xvfb is exclusively for the visible-display mode.
ARG WITH_VNC=false
RUN if [ "$WITH_VNC" = "true" ]; then \
        apt-get update && apt-get install -y --no-install-recommends \
            xvfb \
            fluxbox \
            x11vnc \
            novnc \
            websockify \
        && rm -rf /var/lib/apt/lists/*; \
    fi

WORKDIR /app

# Binary + entrypoint script.
COPY --from=builder /out/twitter /usr/local/bin/twitter
COPY docker/twitter/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Playwright-go Node driver — playwright.Run() at runtime looks at
# $HOME/.cache/ms-playwright-go/<version>/package. Playwright base
# image switches user to pwuser, so copy the driver into pwuser's
# home.
COPY --from=builder /root/.cache/ms-playwright-go /home/pwuser/.cache/ms-playwright-go
RUN chown -R pwuser:pwuser /home/pwuser/.cache

# /data + /config are bind-mount / volume targets. pwuser needs
# write access to profile dir + cookie file. The volume mounts at
# runtime inherit host permissions; the mkdir here ensures the
# standalone `docker run` PoC (no mount) has a writable path.
RUN mkdir -p /data /config && chown -R pwuser:pwuser /data /config

# noVNC's static files are owned by root:root by default; if pwuser
# needs to write the index.html redirect (entrypoint.sh does), give
# it the perms up front. no-op when WITH_VNC=false (dir doesn't exist).
RUN if [ -d /usr/share/novnc ]; then chown -R pwuser:pwuser /usr/share/novnc; fi

USER pwuser
WORKDIR /home/pwuser

# Bind-mount / volume targets.
VOLUME ["/config", "/data"]

# 8888 = twitter service HTTP surface (health, status, search, auth)
# 6080 = noVNC HTTP interface (only meaningful in WITH_VNC=true image;
#        exposed always because Docker EXPOSE is documentation-only —
#        no port is actually opened unless compose publishes it).
EXPOSE 8888 6080

ENV TWITTER_HEADLESS=true \
    TWITTER_SERVICE_ADDR=:8888 \
    TWITTER_PROFILE_DIR=/data/firefox-profile \
    TWITTER_COOKIE_FILE=/config/twitter_cookies.json

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
