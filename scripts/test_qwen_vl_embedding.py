#!/usr/bin/env python3
"""
Path B test for Qwen3-VL-Embedding-{2B,8B} on llama.cpp.

Validates that the llama-server `--embedding --mmproj` setup actually
processes image input — not just text. Reproduces and detects the
failure mode reported in ggml-org/llama.cpp#19525, where the server
silently ignores image data and returns text-only embeddings.

The test:
  1. Send the same image twice → embeddings should be identical
     (sanity check that the endpoint is deterministic).
  2. Send two visually-different images → embeddings should differ
     substantially (cosine << 1.0). If they're effectively identical
     (cosine ~ 1.0), the server is ignoring the image data — known
     failure mode #19525.
  3. Send text only (no image) → should also work; this is the
     "before" capability we don't want to lose.

Usage:
    # Inside a worker container or anywhere with httpx installed
    python scripts/test_qwen_vl_embedding.py \\
        --endpoint http://joi:3104 \\
        --image-a /path/to/soccer-broadcast.jpg \\
        --image-b /path/to/phone-cam.jpg

Exit codes:
    0 — test passed (image data is being processed)
    1 — test failed (image data is being ignored — reproduces #19525)
    2 — endpoint unreachable or other infra failure
"""
from __future__ import annotations

import argparse
import asyncio
import base64
import math
import sys
from pathlib import Path

import httpx


def cosine(a: list[float], b: list[float]) -> float:
    """Plain Python cosine similarity. No numpy dep needed."""
    if len(a) != len(b):
        raise ValueError(f"Dim mismatch: {len(a)} vs {len(b)}")
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(y * y for y in b))
    if na == 0 or nb == 0:
        return 0.0
    return dot / (na * nb)


def load_image_b64(path: Path) -> str:
    with open(path, "rb") as f:
        return base64.b64encode(f.read()).decode("ascii")


async def embed_image(client: httpx.AsyncClient, endpoint: str, image_b64: str) -> list[float]:
    """
    Try both common API shapes:
      1. llama-server native /embedding (multimodal with image_data array)
      2. OpenAI-compatible /v1/embeddings (input as a string or as a multimodal list)

    Whichever returns a valid embedding wins.
    """
    # Shape 1: llama-server native
    try:
        r = await client.post(
            f"{endpoint}/embedding",
            json={"content": "", "image_data": [{"data": image_b64, "id": 10}]},
            timeout=60.0,
        )
        if r.status_code == 200:
            data = r.json()
            # Native shape: {"embedding": [...]} or [{"embedding": [...]}]
            if isinstance(data, list) and data and "embedding" in data[0]:
                return data[0]["embedding"]
            if "embedding" in data:
                return data["embedding"]
    except Exception:
        pass

    # Shape 2: OpenAI-compatible
    try:
        r = await client.post(
            f"{endpoint}/v1/embeddings",
            json={
                "model": "qwen3-vl-embedding",
                "input": [
                    {
                        "content": [
                            {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{image_b64}"}},
                        ],
                    }
                ],
            },
            timeout=60.0,
        )
        if r.status_code == 200:
            data = r.json()
            if "data" in data and data["data"]:
                return data["data"][0]["embedding"]
    except Exception:
        pass

    raise RuntimeError("Could not get embedding from either API shape")


async def embed_text(client: httpx.AsyncClient, endpoint: str, text: str) -> list[float]:
    """Text-only embedding via OpenAI-compatible API."""
    r = await client.post(
        f"{endpoint}/v1/embeddings",
        json={"model": "qwen3-vl-embedding", "input": text},
        timeout=30.0,
    )
    r.raise_for_status()
    data = r.json()
    return data["data"][0]["embedding"]


async def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--endpoint", required=True, help="Base URL of the llama-server (e.g. http://joi:3104)")
    ap.add_argument("--image-a", required=True, type=Path, help="First test image (e.g. soccer broadcast frame)")
    ap.add_argument("--image-b", required=True, type=Path, help="Second test image, visually distinct (e.g. phone-cam frame)")
    ap.add_argument("--ignored-threshold", type=float, default=0.99,
                    help="Above this cosine similarity, we conclude image data is being ignored. Default 0.99.")
    ap.add_argument("--distinct-threshold", type=float, default=0.95,
                    help="Below this cosine similarity, images are reported as cleanly distinct. Default 0.95.")
    args = ap.parse_args()

    if not args.image_a.exists():
        print(f"FAIL: image A not found at {args.image_a}", file=sys.stderr)
        return 2
    if not args.image_b.exists():
        print(f"FAIL: image B not found at {args.image_b}", file=sys.stderr)
        return 2

    img_a_b64 = load_image_b64(args.image_a)
    img_b_b64 = load_image_b64(args.image_b)

    async with httpx.AsyncClient() as client:
        # Sanity: can we reach the server at all?
        try:
            health = await client.get(f"{args.endpoint}/health", timeout=10.0)
            print(f"[health] {health.status_code} {health.text[:200]}")
        except Exception as exc:
            print(f"FAIL: endpoint {args.endpoint} unreachable — {exc}", file=sys.stderr)
            return 2

        # Optional: text-only sanity check (warns but doesn't fail)
        try:
            text_emb = await embed_text(client, args.endpoint, "soccer match goal")
            print(f"[text] embedding dim = {len(text_emb)}; first 4 = {text_emb[:4]}")
        except Exception as exc:
            print(f"WARN: text-only embedding failed — {exc}")

        # Core test: image A twice, image A vs image B
        try:
            emb_a1 = await embed_image(client, args.endpoint, img_a_b64)
            emb_a2 = await embed_image(client, args.endpoint, img_a_b64)
            emb_b = await embed_image(client, args.endpoint, img_b_b64)
        except Exception as exc:
            print(f"FAIL: image embedding failed — {exc}", file=sys.stderr)
            return 2

        print(f"[image] embedding dim = {len(emb_a1)} (expect 2048 for 2B, 4096 for 8B)")

        sim_same = cosine(emb_a1, emb_a2)
        sim_diff = cosine(emb_a1, emb_b)
        print(f"[image] cosine(A, A_again) = {sim_same:.6f}  (should be ~1.0 — deterministic)")
        print(f"[image] cosine(A, B)       = {sim_diff:.6f}  (should be << 1.0 if image data is processed)")

        if sim_same < 0.999:
            print(f"WARN: same-image cosine {sim_same:.6f} < 0.999 — endpoint may be non-deterministic")

        if sim_diff > args.ignored_threshold:
            print()
            print(f"❌ FAIL: cosine(A, B) = {sim_diff:.6f} > {args.ignored_threshold}")
            print("    Server appears to be IGNORING image data.")
            print("    This reproduces ggml-org/llama.cpp#19525.")
            print("    Recommendation: fall back to Path C (sentence-transformers) or wait for upstream support.")
            return 1
        elif sim_diff < args.distinct_threshold:
            print()
            print(f"✓ PASS: cosine(A, B) = {sim_diff:.6f} < {args.distinct_threshold}")
            print("    Images produce distinct embeddings — image data IS being processed.")
            print("    Safe to ship Qwen3-VL-Embedding on this serving stack.")
            return 0
        else:
            print()
            print(f"⚠  AMBIGUOUS: cosine(A, B) = {sim_diff:.6f} between thresholds.")
            print("    Try more visually-distinct images, or compare against a reference Sentence-Transformers")
            print("    embedding of the same images to validate the model is producing useful representations.")
            return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
