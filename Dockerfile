# Prod image for the worker + api binaries. Parameterized on BINARY.
#
# Build with:
#   docker build --build-arg BINARY=worker -t found-footy-worker .
#   docker build --build-arg BINARY=api    -t found-footy-api    .
#
# The twitter binary has its own Dockerfile (docker/twitter/Dockerfile)
# because it needs the Playwright base image + Firefox + optional VNC
# stack — a substantial delta from what worker/api need.

# ────── build stage ──────
# bookworm (not alpine) because we want glibc for CGO-agnostic
# consistency with the twitter Dockerfile's builder — future CGO deps
# (if any) don't ambush us with musl-vs-glibc drift.
FROM golang:1.25.11-bookworm AS build

ARG BINARY
RUN test -n "$BINARY" || (echo "ERROR: BINARY build arg is required (worker|api)" && exit 1)
RUN test "$BINARY" != "twitter" || (echo "ERROR: use docker/twitter/Dockerfile for the twitter binary" && exit 1)

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

# Base runtime deps. ffmpeg is present because the api + worker may
# invoke it for future video pipeline hooks (§14 Phase V); trimming
# it is a Phase-Z cleanup, not worth the branching now.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        ffmpeg \
    && rm -rf /var/lib/apt/lists/*

RUN adduser --disabled-password --gecos "" --uid 1000 app

# Pre-create + own the dirs the non-root `app` user writes at runtime. It can't
# mkdir at / (root-owned), so without this the video scratch dir
# (VIDEO_SCRATCH_DIR, default /scratch) fails "permission denied" the first time
# a clip downloads. Baking it in keeps this a COMPLETE non-root image — no
# runtime chown / env-redirect hacks. (docker.sock access is granted separately
# via the compose `group_add` — the host docker gid isn't bakeable here.)
RUN mkdir -p /scratch && chown app:app /scratch

USER app

COPY --from=build /out/app /usr/local/bin/app

ENTRYPOINT ["/usr/local/bin/app"]
