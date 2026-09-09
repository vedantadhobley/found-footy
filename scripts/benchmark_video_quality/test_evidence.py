"""Regression tests for source identity, independent review labels, and sample accounting."""

import hashlib
import json
import math
from pathlib import Path
import tempfile
import unittest

from evidence import checked_path, sample_times, shared_window, summary
from report import read_complete


class EvidenceTests(unittest.TestCase):
    """Reject bad inputs rather than silently producing misleading quality results."""

    def test_sample_centers_and_invalid_inputs(self):
        """Shared intervals use equal relative positions without touching the next cut."""
        self.assertEqual(sample_times(1, 5, 4), [1.5, 2.5, 3.5, 4.5])
        for start, end in ((-1, 2), (1, 1), (2, 1), (0, math.inf), (math.nan, 2)):
            with self.assertRaises(ValueError):
                sample_times(start, end)
        for count in (0, 65):
            with self.assertRaises(ValueError):
                sample_times(0, 1, count)

    def test_shared_selection_not_quality_fitted(self):
        """Choose the longest sustained section, ignoring primary-route span and scores."""
        row = {"experiment": "aligned-sections-v1", "hash_cadence_ms": 100,
               "routes": [{"route": "primary", "sections": []},
                          {"route": "sustained", "sections": [
                              {"left": {"start": 12, "end": 40}, "right": {"start": 0, "end": 28}},
                              {"left": {"start": 45, "end": 90}, "right": {"start": 33, "end": 78}}]}]}
        self.assertEqual(shared_window(row), {"left": (4.5, 9), "right": (3.3, 7.8)})
        row["hash_cadence_ms"] = None
        with self.assertRaises(ValueError):
            shared_window(row)

    def test_source_integrity_and_path_escape(self):
        """A saved source changing or escaping the mounted corpus fails closed."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "clip.mp4"
            source.write_bytes(b"fixture")
            expected = hashlib.sha256(b"fixture").hexdigest()
            self.assertEqual(checked_path(root, "clip.mp4", expected), source)
            with self.assertRaises(ValueError):
                checked_path(root, "clip.mp4", "0" * 64)
            sub = root / "sub"
            sub.mkdir()
            with self.assertRaises(ValueError):
                checked_path(sub, "../clip.mp4", expected)

    def test_summary_and_invalid_predictions(self):
        """Every raw score survives separately from a finite median/mean summary."""
        self.assertEqual(summary([5, 1, 3, 2])["median"], 2.5)
        self.assertEqual(summary([5, 1, 3])["median"], 3)
        for values in ([], [math.nan], [math.inf]):
            with self.assertRaises(ValueError):
                summary(values)

    def test_tentative_labels_cannot_authorize_replacement(self):
        """Review-page order is resolved by immutable asset ID, not corpus side."""
        notes = json.loads((Path(__file__).parent / "review-notes.json").read_text())
        by_pair = {n["pair_id"]: n for n in notes["notes"]}
        self.assertEqual(by_pair["palacios-42"]["preferred_asset_id"], "a2e66274-1978-5e92-b8b8-94661627114e")
        self.assertEqual(by_pair["mariano-36"]["preferred_asset_id"], "9f37ee38-4df8-595f-86c4-f61e29f73388")
        for note in notes["notes"]:
            self.assertEqual(note["status"], "tentative_presentation_preference")
            self.assertEqual(note["replacement_decision"], "unresolved")

    def test_report_requires_complete_unique_evidence(self):
        """Partial runs and duplicate records cannot masquerade as a benchmark report."""
        rows = [{"type": "environment"},
                {"type": "score", "job_id": "one", "view": "full"},
                {"type": "score", "job_id": "one", "view": "center"},
                {"status": "benchmark_complete"}]
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "scores.ndjson"
            path.write_text("\n".join(json.dumps(row) for row in rows))
            scores, _ = read_complete(path, [{"id": "one"}])
            self.assertEqual(len(scores), 2)
            with self.assertRaises(ValueError):
                read_complete(path, [{"id": "one"}], "unexpected-manifest")
            for broken in (rows[:-1], rows[:2] + rows[-1:], rows[:2] + rows[1:]):
                path.write_text("\n".join(json.dumps(row) for row in broken))
                with self.assertRaises(ValueError):
                    read_complete(path, [{"id": "one"}])


if __name__ == "__main__":
    unittest.main()
