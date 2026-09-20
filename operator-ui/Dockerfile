# Standalone image for the removable Found Footy operator video browser.
FROM golang:1.25.11-bookworm AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/operator-ui ./cmd/operator-ui

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && adduser --disabled-password --gecos "" --uid 1000 app
USER app
COPY --from=build /out/operator-ui /usr/local/bin/operator-ui
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --retries=3 CMD ["/usr/local/bin/operator-ui", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/operator-ui"]
