"""Run the reproducible local-only image-quality baseline and control generation."""

import argparse
import json
from pathlib import Path
import resource
import subprocess
import sys
import time

import cv2

from evidence import MODEL_HASHES, checked_path, digest, load_jobs, sample_times, summary


CONTROLS = {
    "baseline": (None, "ffv1", "reference_for_transform_only"),
    "blur_mild": ("gblur=sigma=1", "ffv1", "distortion"),
    "blur_strong": ("gblur=sigma=3", "ffv1", "distortion"),
    "downscale_upscale": ("scale=320:180:flags=area,scale=1280:720:flags=bilinear", "ffv1", "distortion"),
    "compression_mild": (None, "h264-28", "distortion"),
    "compression_strong": (None, "h264-40", "distortion"),
    "noise": ("noise=alls=8:allf=t:all_seed=17", "ffv1", "stress_not_human_label"),
    "sharpen": ("unsharp=5:5:1.5:5:5:0", "ffv1", "stress_not_human_label"),
    "overlay": ("drawbox=x=iw/3:y=ih/2:w=iw/3:h=ih/8:color=white@0.7:t=fill", "ffv1", "stress_not_human_label"),
    "repeated_30_in_60": ("fps=30,fps=60", "ffv1", "cadence_not_image_target"),
}


def emit(value):
    """Write structured progress/results with explicit completion markers."""
    print(json.dumps(value, allow_nan=False), flush=True)


def prepare(args):
    """Freeze verified jobs and deterministic transformed copies in a fresh run directory."""
    jobs = load_jobs(args.corpus, args.overlap, args.media)
    target = Path(args.output)
    target.mkdir(parents=True, exist_ok=False)
    control_dir = target / "controls"
    control_dir.mkdir()
    for pair_id, side in (("adams-82", "right"), ("palacios-42", "left")):
        source = next(j for j in jobs if j["pair_id"] == pair_id and j["side"] == side and j["scope"] == "whole")
        for name, (filters, codec, expectation) in CONTROLS.items():
            path = control_dir / f"{pair_id}-{name}.mkv"
            command = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-n",
                       "-threads", "2", "-i", source["path"], "-t", "4", "-an", "-sn"]
            if filters:
                command += ["-vf", filters]
            if codec.startswith("h264"):
                command += ["-c:v", "libx264", "-preset", "medium", "-crf", codec.split("-")[1]]
            else:
                command += ["-c:v", "ffv1", "-level", "3"]
            command += ["-threads", "2", "-filter_threads", "1", "-fps_mode", "passthrough", str(path)]
            subprocess.run(command, check=True, timeout=120, stdout=subprocess.DEVNULL)
            jobs.append({"id": f"{pair_id}-control-{name}", "pair_id": pair_id, "side": side,
                         "scope": "control", "control": name, "expectation": expectation,
                         "path": str(path), "source_sha256": digest(path),
                         "parent_sha256": source["source_sha256"], "start": 0, "end": 4,
                         "human": None, "width": source["width"], "height": source["height"],
                         "frame_rate": source["frame_rate"]})
            emit({"generated": path.name})
    manifest = {"schema_version": 1, "corpus_sha256": digest(args.corpus),
                "overlap_sha256": digest(args.overlap), "samples_per_scope": 8,
                "jobs": jobs, "review_notes": json.loads(Path(args.review_notes).read_text())}
    (target / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    emit({"status": "prepare_complete", "jobs": len(jobs), "manifest": str(target / "manifest.json")})


def frames_for(job, count):
    """Decode deterministic native-resolution samples; never use normalized dHash images."""
    if digest(job["path"]) != job["source_sha256"]:
        raise ValueError("job media changed after preparation")
    cap = cv2.VideoCapture(job["path"])
    if not cap.isOpened():
        raise ValueError("cannot open saved video")
    frames, positions = [], []
    try:
        for seconds in sample_times(job["start"], job["end"], count):
            if not cap.set(cv2.CAP_PROP_POS_MSEC, seconds * 1000):
                raise ValueError("decoder does not support requested seek")
            ok, frame = cap.read()
            if not ok or frame is None:
                raise ValueError("missing decoded sample")
            actual = cap.get(cv2.CAP_PROP_POS_MSEC) / 1000
            if abs(actual - seconds) > 0.11:
                raise ValueError(f"decoded sample outside timing tolerance: {seconds} -> {actual}")
            frames.append(frame)
            positions.append({"requested": seconds, "decoded": actual})
    finally:
        cap.release()
    return frames, positions


def run(args):
    """Score all jobs on CPU, retaining raw sample distributions and resource evidence."""
    cv2.setNumThreads(2)
    manifest = json.loads(Path(args.manifest).read_text())
    root = Path(args.models)
    paths = {name: checked_path(root, name, sha) for name, sha in MODEL_HASHES.items()}
    run_started = started = time.perf_counter()
    model = cv2.quality.QualityBRISQUE_create(str(paths["brisque_model_live.yml"]), str(paths["brisque_range_live.yml"]))
    emit({"type": "environment", "metric": "opencv-brisque-live", "lower_better": True,
          "python": sys.version, "opencv": cv2.__version__, "model_hashes": MODEL_HASHES,
          "model_load_ms": (time.perf_counter() - started) * 1000,
          "manifest_sha256": digest(args.manifest), "cpu_threads": 2})
    for job in manifest["jobs"]:
        started = time.perf_counter()
        frames, positions = frames_for(job, manifest["samples_per_scope"])
        decoded = time.perf_counter()
        # Full native frames are the preregistered baseline. Center cropping is
        # a sensitivity diagnostic only and must not erase watermark evidence.
        for view in ("full", "center"):
            tick = time.perf_counter()
            scores = []
            for frame in frames:
                if view == "center":
                    h, w = frame.shape[:2]
                    frame = frame[int(h*.2):int(h*.85), int(w*.1):int(w*.9)]
                scores.append(float(model.compute(frame)[0]))
            emit({"type": "score", "metric": "opencv-brisque-live", "lower_better": True,
                  "job_id": job["id"], "pair_id": job["pair_id"], "side": job["side"],
                  "scope": job["scope"], "control": job.get("control"), "view": view,
                  "scores": scores, "summary": summary(scores), "positions": positions,
                  "decode_ms": (decoded - started) * 1000,
                  "inference_ms": (time.perf_counter() - tick) * 1000,
                  "peak_process_rss_mib": resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024})
    peak_path = Path("/sys/fs/cgroup/memory.peak")
    container_peak = int(peak_path.read_text()) / (1024 * 1024) if peak_path.is_file() else None
    emit({"status": "benchmark_complete", "jobs": len(manifest["jobs"]),
          "metric": "opencv-brisque-live", "wall_seconds": time.perf_counter() - run_started,
          "container_peak_mib": container_peak})


def main():
    """Expose local-only phases; failed work never emits a success marker."""
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prep = commands.add_parser("prepare")
    prep.add_argument("--corpus", default="/corpus/cadence-pairs-2026-09-08.json")
    prep.add_argument("--overlap", default="/evidence/overlap-natural.ndjson")
    prep.add_argument("--media", default="/evidence/media")
    prep.add_argument("--review-notes", default="/tool/review-notes.json")
    prep.add_argument("--output", required=True)
    score = commands.add_parser("score")
    score.add_argument("--manifest", required=True)
    score.add_argument("--models", default="/output/downloads")
    args = parser.parse_args()
    (prepare if args.command == "prepare" else run)(args)


if __name__ == "__main__":
    main()
