# Use Prefect 3 as base image
FROM prefecthq/prefect:3-python3.10

# Create app user
ARG USER_ID=1000
ARG GROUP_ID=1000
RUN groupadd -g ${GROUP_ID} appuser && \
    useradd -m -u ${USER_ID} -g ${GROUP_ID} appuser

WORKDIR /app

# Install uv for fast dependency management
RUN pip install uv

# Copy and install dependencies (for better caching)
COPY requirements.txt .
RUN uv pip install --system -r requirements.txt

# Copy application code
COPY . .

# Create downloads directory with proper permissions
RUN mkdir -p /app/downloads && chown -R appuser:appuser /app/downloads

# Switch to non-root user
USER appuser

# Only create deployments, no immediate run
CMD ["python", "-m", "found_footy.flows.deployments", "--apply"]