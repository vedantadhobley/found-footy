"""Summarize complete pilot evidence without converting preferences into replacement policy."""

import argparse
import json
from pathlib import Path

from evidence import digest


def read_complete(path, jobs, manifest_sha256=None):
    """Require one complete measurement for each job/view, not just process exit."""
    rows = [json.loads(line) for line in Path(path).read_text().splitlines()]
    if not rows or rows[-1].get("status") != "benchmark_complete":
        raise ValueError("missing benchmark completion marker")
    if manifest_sha256 is not None and rows[0].get("manifest_sha256") != manifest_sha256:
        raise ValueError("result/manifest provenance mismatch")
    scores = [r for r in rows if r.get("type") == "score"]
    keyed = {(r["job_id"], r["view"]): r for r in scores}
    expected = {(job["id"], view) for job in jobs for view in ("full", "center")}
    if len(keyed) != len(scores) or set(keyed) != expected:
        raise ValueError("missing/duplicate score rows")
    return keyed, rows[0]


def main():
    """Emit a small Markdown report with scores, controls, and measured process cost."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest")
    parser.add_argument("results")
    args = parser.parse_args()
    manifest = json.loads(Path(args.manifest).read_text())
    jobs = manifest["jobs"]
    scores, environment = read_complete(args.results, jobs, digest(args.manifest))
    print("# BRISQUE offline pilot\n")
    print("Lower scores predict better image quality. These are not replacement decisions.\n")
    print("| Pair | Scope | View | Left median | Right median | Predicted preference |")
    print("|---|---|---|---:|---:|---|")
    pairs = sorted({j["pair_id"] for j in jobs if j["scope"] != "control"})
    for pair in pairs:
        for scope in ("whole", "shared"):
            for view in ("full", "center"):
                left = scores[(f"{pair}-left-{scope}", view)]["summary"]["median"]
                right = scores[(f"{pair}-right-{scope}", view)]["summary"]["median"]
                preference = "left" if left < right else "right" if right < left else "tie"
                print(f"| {pair} | {scope} | {view} | {left:.3f} | {right:.3f} | {preference} |")
    print("\n## Controls\n")
    print("Scores are full-frame medians. Noise/sharpening/overlay are stress cases, not accepted human labels.\n")
    print("| Source | Transform | Score | Difference from lossless baseline |")
    print("|---|---|---:|---:|")
    for job in jobs:
        if job["scope"] != "control":
            continue
        result = scores[(job["id"], "full")]["summary"]["median"]
        base = scores[(f"{job['pair_id']}-control-baseline", "full")]["summary"]["median"]
        print(f"| {job['pair_id']} | {job['control']} | {result:.3f} | {result-base:+.3f} |")
    print("\n## Cost\n")
    unique = [scores[(job["id"], "full")] for job in jobs]
    print(f"- CPU thread limit: {environment['cpu_threads']}.")
    print(f"- Model load: {environment['model_load_ms']:.1f} ms.")
    print(f"- Sample decode total (once per job): {sum(s['decode_ms'] for s in unique)/1000:.2f} s.")
    print(f"- Inference total (both views): {sum(s['inference_ms'] for s in scores.values())/1000:.2f} s.")
    print(f"- Peak scorer-process RSS: {max(s['peak_process_rss_mib'] for s in unique):.1f} MiB.")
    print("- Process RSS is not container peak memory; source generation is timed separately.")


if __name__ == "__main__":
    main()
