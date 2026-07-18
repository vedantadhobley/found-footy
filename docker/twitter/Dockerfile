# Twitter service container — Playwright-Go + Firefox.
#
# Two-stage build:
#   1. builder: golang:1.25 compiles the twitter binary
#   2. runner: mcr.microsoft.com/playwright:v1.61.1-noble has Firefox
#      + Playwright driver + all Firefox runtime deps pre-installed.
#      We just install the driver at the version playwright-go
#      v0.6100.0 expects, drop the binary in, and set the entrypoint.
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
# playwright-go v0.6100.0 pairs with Playwright driver v1.47.0.
# Building with VCS ignoring flag because container layers can't run git.
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -o /out/twitter ./cmd/twitter

# Install the playwright-go driver (Node-based, ~30MB) into the
# location playwright.Run() looks for it: ~/.cache/ms-playwright-go.
# Firefox itself comes from the Microsoft playwright base image in
# the runtime stage; only the driver needs to be here.
RUN go run github.com/mxschmitt/playwright-go/cmd/playwright@v0.6100.0 install --dry-run || true
RUN CGO_ENABLED=0 go build -buildvcs=false -o /out/playwright-install \
    github.com/mxschmitt/playwright-go/cmd/playwright
RUN mkdir -p /root/.cache && /out/playwright-install install firefox || true
RUN ls -la /root/.cache/ 2>&1 || true

# Runtime: official Playwright image v1.61.1-noble ships browsers +
# system libs matching playwright-go v0.6100.0's driver version.
FROM mcr.microsoft.com/playwright:v1.61.1-noble
WORKDIR /app

COPY --from=builder /out/twitter /usr/local/bin/twitter

# Copy the pre-installed playwright-go Node driver from the builder
# stage into pwuser's home. playwright.Run() at runtime looks at
# $HOME/.cache/ms-playwright-go/<version>/package. Skipping this
# step means the runtime tries to download the driver on first Run(),
# which stalls container startup on network + fails offline.
COPY --from=builder /root/.cache/ms-playwright-go /home/pwuser/.cache/ms-playwright-go
RUN chown -R pwuser:pwuser /home/pwuser/.cache

# /data + /config are bind-mount targets; pwuser needs write access
# to the profile dir. Volume-mounted dirs at runtime inherit their
# host permissions, but for the standalone PoC (no mount) we create
# a writable stub inside the image.
RUN mkdir -p /data /config && chown -R pwuser:pwuser /data /config

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
