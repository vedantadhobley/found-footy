# Headless Playwright-Go + Firefox search-service container.
#
# Build examples:
#   docker build -f docker/twitter/Dockerfile -t found-footy-twitter .

# ────── build stage ──────
# Dedicated to the twitter search binary. Operator login has its own raw-
# Firefox image and does not share this Playwright build.
FROM golang:1.25.11-bookworm AS builder

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
    ./cmd/twitter

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
# Microsoft's Playwright image ships Firefox + all Playwright system libraries
# matched to driver v1.61.1. Operator login uses docker/twitter-auth instead.
FROM mcr.microsoft.com/playwright:v1.61.1-noble

WORKDIR /app

# Search-service binary.
COPY --from=builder /out/twitter /usr/local/bin/twitter

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

USER pwuser
WORKDIR /home/pwuser

# Bind-mount / volume targets.
VOLUME ["/config", "/data"]

# 8888 = Twitter search-service HTTP surface.
EXPOSE 8888

ENV TWITTER_HEADLESS=true \
    TWITTER_SERVICE_ADDR=:8888 \
    TWITTER_PROFILE_DIR=/data/firefox-profile \
    TWITTER_COOKIE_FILE=/config/twitter_cookies.json

ENTRYPOINT ["/usr/local/bin/twitter"]
