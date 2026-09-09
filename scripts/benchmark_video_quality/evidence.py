"""Pure provenance, sampling, and summary rules for the offline quality pilot."""

import hashlib
import json
import math
from pathlib import Path


MODEL_HASHES = {
    "brisque_model_live.yml": "4c44c7eec9e5139830c0bb0e4a044c9cd351ba5bbc9e3798beca3243e2782ff4",
    "brisque_range_live.yml": "a18427f4f7ad087524bd0d63389649a3899b51ca6d44a4d5f8d8171f1f914a9c",
}


def digest(path):
    """Hash bounded regular input bytes, without modifying the source."""
    path = Path(path)
    if not path.is_file() or path.stat().st_size > 256 * 1024 * 1024:
        raise ValueError(f"not a bounded regular media/model file: {path}")
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def checked_path(root, name, expected):
    """Reject path escapes and changed artifacts before producing measurements."""
    root = Path(root).resolve()
    path = (root / name).resolve()
    if not path.is_relative_to(root) or digest(path) != expected:
        raise ValueError(f"source path or SHA-256 mismatch: {name}")
    return path


def sample_times(start, end, count=8):
    """Sample bin centers to avoid rounding beyond the end of a shared section."""
    if not all(math.isfinite(x) for x in (start, end)) or start < 0 or end <= start:
        raise ValueError("invalid sample interval")
    if count < 1 or count > 64:
        raise ValueError("sample count outside [1,64]")
    return [start + (i + 0.5) * (end - start) / count for i in range(count)]


def shared_window(row):
    """Use the longest sustained-route section, never a fitted quality winner."""
    if row.get("experiment") != "aligned-sections-v1" or row.get("hash_cadence_ms") != 100:
        raise ValueError("unsupported overlap evidence")
    routes = [r for r in row["routes"] if r["route"] == "sustained"]
    if len(routes) != 1 or not routes[0]["sections"]:
        raise ValueError("pilot requires a sustained qualified section")
    section = max(routes[0]["sections"], key=lambda s: s["left"]["end"] - s["left"]["start"])
    left, right = section["left"], section["right"]
    if left["end"] - left["start"] != right["end"] - right["start"]:
        raise ValueError("unequal aligned section spans")
    result = {side: (section[side]["start"] / 10, section[side]["end"] / 10)
              for side in ("left", "right")}
    for start, end in result.values():
        sample_times(start, end)
    return result


def summary(values):
    """Keep distributions; no confidence claim or cross-metric score fusion."""
    ordered = sorted(float(v) for v in values)
    if not ordered or not all(math.isfinite(v) for v in ordered):
        raise ValueError("empty or non-finite model output")
    middle = len(ordered) // 2
    median = ordered[middle] if len(ordered) % 2 else (ordered[middle-1] + ordered[middle]) / 2
    return {"mean": sum(ordered) / len(ordered), "median": median,
            "min": ordered[0], "max": ordered[-1], "samples": len(ordered)}


def load_jobs(corpus_path, overlap_path, media_root):
    """Build paired whole/shared jobs from source-verified saved evidence."""
    corpus = json.loads(Path(corpus_path).read_text())
    overlaps = [json.loads(line) for line in Path(overlap_path).read_text().splitlines()]
    indexed = {row["pair_id"]: row for row in overlaps}
    ids = [pair["id"] for pair in corpus["cases"]]
    if (len(indexed) != len(overlaps) or len(corpus["cases"]) != 5
            or len(set(ids)) != len(ids) or set(ids) != set(indexed)):
        raise ValueError("duplicate/missing pilot evidence")
    jobs = []
    for pair in corpus["cases"]:
        row = indexed[pair["id"]]
        windows = shared_window(row)
        for side in ("left", "right"):
            asset = pair[side]
            if row[side]["asset_id"] != asset["asset_id"] or row[side]["sha256"] != asset["sha256"]:
                raise ValueError("overlap/corpus asset drift")
            path = checked_path(media_root, asset["asset_id"] + ".mp4", asset["sha256"])
            duration = asset["duration_ms"] / 1000
            for scope, bounds in (("whole", (0, duration)), ("shared", windows[side])):
                if not 0 <= bounds[0] < bounds[1] <= duration:
                    raise ValueError("section outside source duration")
                jobs.append({"id": f"{pair['id']}-{side}-{scope}", "pair_id": pair["id"],
                             "side": side, "scope": scope, "path": str(path),
                             "source_sha256": asset["sha256"], "asset_id": asset["asset_id"],
                             "start": bounds[0], "end": bounds[1],
                             "human": pair.get("human"), "width": asset["width"],
                             "height": asset["height"], "frame_rate": asset["frame_rate"]})
    return jobs
