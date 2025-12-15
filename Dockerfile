# Production Dockerfile - code is baked into image
FROM python:3.10-slim

# Install system dependencies
RUN apt-get update && apt-get install -y \
    git \
    curl \
    wget \
    build-essential \
    gnupg \
    # Firefox for browser automation
    firefox-esr \
    xvfb \
    # VNC server and noVNC for web-based GUI access
    x11vnc \
    fluxbox \
    novnc \
    websockify \
    # ffmpeg for video processing
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# Install Google Chrome (for undetected-chromedriver)
RUN wget -q -O - https://dl.google.com/linux/linux_signing_key.pub | gpg --dearmor -o /usr/share/keyrings/google-chrome.gpg \
    && echo "deb [arch=amd64 signed-by=/usr/share/keyrings/google-chrome.gpg] http://dl.google.com/linux/chrome/deb/ stable main" > /etc/apt/sources.list.d/google-chrome.list \
    && apt-get update \
    && apt-get install -y google-chrome-stable \
    && rm -rf /var/lib/apt/lists/*

# Install geckodriver for Firefox automation
RUN wget -q https://github.com/mozilla/geckodriver/releases/download/v0.35.0/geckodriver-v0.35.0-linux64.tar.gz \
    && tar -xzf geckodriver-v0.35.0-linux64.tar.gz \
    && mv geckodriver /usr/local/bin/ \
    && chmod +x /usr/local/bin/geckodriver \
    && rm geckodriver-v0.35.0-linux64.tar.gz

# Set working directory
WORKDIR /app

# Copy requirements and install dependencies first (better layer caching)
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application code into image
COPY src/ ./src/
COPY twitter/ ./twitter/

# Environment variables
ENV PYTHONPATH=/app \
    CHROME_BIN=/usr/bin/google-chrome \
    DISPLAY=:99 \
    VNC_PORT=5900 \
    NOVNC_PORT=6080

# Default command (overridden in docker-compose)
CMD ["python", "/app/src/worker.py"]
