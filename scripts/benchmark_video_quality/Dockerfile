# CPU-only offline quality benchmark; no production binary or service entrypoint.
FROM python@sha256:158caf0e080e2cd74ef2879ed3c4e697792ee65251c8208b7afb56683c32ea6c
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 OMP_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2 MKL_NUM_THREADS=2
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg time \
    && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir numpy==1.26.4 opencv-contrib-python-headless==4.11.0.86
WORKDIR /tool
ENTRYPOINT ["python", "benchmark.py"]
