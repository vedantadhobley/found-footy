# Prod image for all four Go binaries. Parameterized on BINARY.
#
# Build with:
#   docker build --build-arg BINARY=worker -t found-footy-worker .
#   docker build --build-arg BINARY=api    -t found-footy-api    .
#   docker build --build-arg BINARY=scaler -t found-footy-scaler .
#   docker build --build-arg BINARY=twitter -t found-footy-twitter .
#
# docker-compose.yml passes BINARY per service. See §10.

# ────── build stage ──────
# bookworm (not alpine) because the twitter binary target needs glibc
# for Playwright's Firefox launcher. Same base for all four binaries;
# cheaper than juggling two build images.
FROM golang:1.25-bookworm AS build

ARG BINARY
RUN test -n "$BINARY" || (echo "ERROR: BINARY build arg is required (worker|api|scaler|twitter)" && exit 1)

WORKDIR /src

# Cache dependencies separately from source so a .go file edit doesn't
# re-download modules.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# §11 deploy tracking — bake identity into the binary via ldflags so
# every log line + Prometheus deploy_info_gauge can report it.
ARG GIT_SHA=unknown
ARG BUILT_AT=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -buildvcs=false \
    -ldflags="-s -w -X main.gitSHA=${GIT_SHA} -X main.builtAt=${BUILT_AT}" \
    -o /out/app \
    ./cmd/${BINARY}

# ────── runtime stage ──────
FROM debian:bookworm-slim

ARG BINARY
ENV BINARY=${BINARY}

# Base runtime deps that every binary needs.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# Twitter target: add Firefox + Xvfb for Playwright headless browser.
# Skipped for other binaries (BINARY != "twitter" → the RUN is a no-op
# per the shell `if` — no layer growth beyond a few KB).
RUN if [ "$BINARY" = "twitter" ]; then \
        apt-get update && apt-get install -y --no-install-recommends \
            firefox-esr \
            xvfb \
        && rm -rf /var/lib/apt/lists/*; \
    fi

# noVNC layer — only for the twitter-vnc container (docker-compose "vnc"
# profile). Adds ~100 MB, so we gate it behind a build arg rather than
# baking into every twitter image.
ARG WITH_VNC=false
RUN if [ "$WITH_VNC" = "true" ]; then \
        apt-get update && apt-get install -y --no-install-recommends \
            x11vnc \
            novnc \
            websockify \
        && rm -rf /var/lib/apt/lists/*; \
    fi

RUN adduser --disabled-password --gecos "" --uid 1000 app
USER app

COPY --from=build /out/app /usr/local/bin/app

ENTRYPOINT ["/usr/local/bin/app"]
