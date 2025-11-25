# Use Prefect 3 as base image
FROM prefecthq/prefect:3-python3.10

# Create app user
ARG USER_ID=1000
ARG GROUP_ID=1000
RUN groupadd -g ${GROUP_ID} appuser && \
    useradd -m -u ${USER_ID} -g ${GROUP_ID} appuser

WORKDIR /app

# ✅ Install Chrome for browser automation (multi-arch support)
RUN apt-get update && apt-get install -y \
    wget \
    ca-certificates \
    curl \
    openssl \
    git \
    build-essential \
    libssl-dev \
    libffi-dev \
    python3-dev \
    # ✅ ADD: Network tools for debugging
    iputils-ping \
    dnsutils \
    # Chrome dependencies
    chromium \
    chromium-driver \
    xvfb \
    # ✅ ADD: gosu for switching users in entrypoint
    gosu \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# ✅ Create Chrome wrapper script for headless operation
RUN echo '#!/bin/bash\nexec chromium "$@" --no-sandbox --disable-dev-shm-usage --headless --disable-gpu' > /usr/local/bin/chrome \
    && chmod +x /usr/local/bin/chrome

# Install certificates and uv
RUN update-ca-certificates --fresh && \
    mkdir -p /etc/ssl/custom && \
    cat /etc/ssl/certs/ca-certificates.crt > /etc/ssl/custom/bundle.crt && \
    pip install uv

# Copy requirements and install dependencies
COPY requirements.txt .
RUN PYTHONHTTPSVERIFY=0 uv pip install --system -r requirements.txt

# Copy application code and set permissions
COPY . .
RUN chown -R appuser:appuser /app && \
    mkdir -p /app/downloads && chown -R appuser:appuser /app/downloads && \
    mkdir -p /app/dagster_logs /app/dagster_storage && \
    chown -R appuser:appuser /app/dagster_logs /app/dagster_storage

# Switch to non-root user
USER appuser

# Environment variables - ✅ REMOVE HARDCODED CREDENTIALS
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
ENV SSL_CERT_DIR=/etc/ssl/certs
ENV PYTHONHTTPSVERIFY=0
ENV PYTHONPATH=/app
ENV CURL_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
ENV REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
# Chrome environment
ENV CHROME_BIN=/usr/local/bin/chrome
ENV CHROMEDRIVER_PATH=/usr/bin/chromedriver

# ✅ DEFAULT: Start worker (init container overrides this)
CMD ["prefect", "worker", "start", "--pool", "default-pool", "--type", "process"]