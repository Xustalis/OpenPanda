# SPDX-License-Identifier: AGPL-3.0-or-later
import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("diagnostics", Path(__file__).resolve().parents[1] / "scripts/execution-diagnostics.py")
diagnostics = importlib.util.module_from_spec(spec)
spec.loader.exec_module(diagnostics)


class DiagnosticsTest(unittest.TestCase):
    def test_read_only_filtered_metrics_do_not_export_raw_payloads(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "runtime #1.sqlite"
            with sqlite3.connect(path) as db:
                db.executescript("CREATE TABLE tasks(task_id,state,created_at,updated_at,result_json,plan_id); CREATE TABLE task_events(id,task_id,ts,type,data_json); CREATE TABLE task_checks(task_id);")
                db.executemany("INSERT INTO tasks VALUES (?,?,?,?,?,?)", [
                    ("a", "done", 10, 50, json.dumps({"needs_followup": True, "stdout": "PRIVATE"}), ""),
                    ("b", "failed", 60, 70, "{}", ""),
                ])
                payloads = [
                    ("agent_event", {"ev": "tool_use", "name": "execute", "input": {"code": "PRIVATE"}}),
                    ("supervise", {"status": "continue", "reason": "PRIVATE"}),
                    ("agent_usage", {"input_tokens": 12, "output_tokens": 3}),
                    ("adapter_execution_profile", {"attempt_id": "x", "profile": {"transport": "session_start"}}),
                    ("adapter_execution_profile", {"attempt_id": "x", "profile": {"transport": "one_shot", "resume_requested": False}}),
                    ("review", {"approved": True, "resumed": False}),
                ]
                db.executemany("INSERT INTO task_events VALUES (?,?,?,?,?)", [(i, "a", 10+i, kind, json.dumps(data)) for i, (kind, data) in enumerate(payloads, 1)])
            before = path.read_bytes()
            result = diagnostics.collect(path, 10, 60)
            self.assertEqual(before, path.read_bytes())
            self.assertEqual(result["summary"]["tasks"], 1)
            self.assertEqual(result["summary"]["done_with_unfinished_evidence"], 1)
            task = result["tasks"][0]
            self.assertEqual(task["tool_calls"], 1)
            self.assertEqual(task["lifecycle_seconds"], 40)
            self.assertEqual(task["fresh_restart_after_session_event_ids"], [5])
            self.assertEqual(task["approval_without_resume_event_ids"], [6])
            self.assertEqual(task["observed_agent_usage"]["input_tokens"], 12)
            self.assertNotIn("PRIVATE", json.dumps(result))

    def test_missing_database_is_not_created(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "absent.sqlite"
            with self.assertRaises(sqlite3.OperationalError):
                diagnostics.collect(path)
            self.assertFalse(path.exists())


if __name__ == "__main__":
    unittest.main()
