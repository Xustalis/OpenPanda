#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Read-only execution metrics; deliberately excludes prompts and tool bodies.

Usage: python3 scripts/execution-diagnostics.py --db /path/to/openpanda.db
       [--since UNIX_SECONDS] [--until UNIX_SECONDS]

The cohort uses task creation time, with an exclusive upper bound. Durations
include queue/review time. Usage is observed event usage, not a billing total.
No task is executed, approved, migrated, or modified by this script.
"""
import argparse
import collections
import json
from pathlib import Path
import re
import sqlite3


def object_json(raw):
    try:
        value = json.loads(raw or "{}")
    except (ValueError, TypeError):
        return {}
    return value if isinstance(value, dict) else {}


def count(value):
    return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else 0


def collect(db_path, since=0, until=2**63 - 1):
    uri = Path(db_path).expanduser().resolve().as_uri() + "?mode=ro"
    conn = sqlite3.connect(uri, uri=True)
    conn.row_factory = sqlite3.Row
    try:
        conn.execute("PRAGMA query_only=ON")
        conn.execute("BEGIN")
        tasks = conn.execute(
            "SELECT task_id,state,created_at,updated_at,result_json,plan_id "
            "FROM tasks WHERE created_at>=? AND created_at<? ORDER BY created_at,task_id",
            (since, until),
        ).fetchall()
        tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        metrics = []
        for task in tasks:
            task_id = task["task_id"]
            events = conn.execute(
                "SELECT id,ts,type,data_json FROM task_events WHERE task_id=? ORDER BY id",
                (task_id,),
            ).fetchall()
            tools = collections.Counter()
            verdicts = collections.Counter()
            usage = collections.Counter()
            first_tool = None
            session_attempts = set()
            restarts, approval_without_resume, supervision_ids = [], [], []
            for event in events:
                data = object_json(event["data_json"])
                kind = event["type"]
                if kind == "agent_event" and data.get("ev") == "tool_use":
                    name = data.get("name", "unknown")
                    if not isinstance(name, str) or not re.fullmatch(r"[A-Za-z0-9_.:/-]{1,80}", name):
                        name = "other"
                    tools[name] += 1
                    if first_tool is None:
                        first_tool = event["ts"]
                elif kind == "supervise":
                    status = data.get("status")
                    verdicts[status if status in ("done", "continue", "review", "blocked") else "other"] += 1
                    supervision_ids.append(event["id"])
                elif kind == "agent_usage":
                    usage["events"] += 1
                    for key in ("input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"):
                        usage[key] += count(data.get(key))
                elif kind == "adapter_execution_profile":
                    profile = data.get("profile")
                    if not isinstance(profile, dict):
                        continue
                    attempt = data.get("attempt_id")
                    if not isinstance(attempt, str) or not attempt:
                        continue
                    if profile.get("transport") == "session_start":
                        session_attempts.add(attempt)
                    elif profile.get("transport") == "one_shot" and attempt in session_attempts and profile.get("resume_requested") is False:
                        restarts.append(event["id"])
                elif kind == "review" and data.get("approved") is True and data.get("resumed") is False:
                    approval_without_resume.append(event["id"])
            result = object_json(task["result_json"])
            unfinished = result.get("needs_followup") is True or result.get("verdict") in ("continue", "blocked")
            check_count = None
            if "task_checks" in tables:
                check_count = conn.execute("SELECT count(*) FROM task_checks WHERE task_id=?", (task_id,)).fetchone()[0]
            metrics.append({
                "task_id": task_id, "state": task["state"],
                "created_at": task["created_at"], "updated_at": task["updated_at"],
                "lifecycle_seconds": task["updated_at"] - task["created_at"],
                "first_observed_tool_after_seconds": None if first_tool is None else first_tool - task["created_at"],
                "event_count": len(events), "tool_calls": sum(tools.values()),
                "tools_by_name": dict(sorted(tools.items())),
                "supervision_verdicts": dict(sorted(verdicts.items())),
                "supervision_event_ids": supervision_ids,
                "observed_agent_usage": dict(sorted(usage.items())),
                "fresh_restart_after_session_event_ids": restarts,
                "approval_without_resume_event_ids": approval_without_resume,
                "done_with_unfinished_evidence": task["state"] == "done" and unfinished,
                "has_plan": bool(task["plan_id"]), "task_checks": check_count,
            })
        return {
            "schema_version": 1,
            "cohort": {"created_at_gte": since, "created_at_lt": until},
            "limitations": [
                "Lifecycle duration includes queue, review and operator time.",
                "Usage and tool counts describe observed events; absent events are not proof of no work.",
                "Fresh restart identifies a profile sequence, not proven duplicate side effects.",
                "Plan IDs do not prove process concurrency or successful dependency transfer.",
                "No matched native-harness performance or quality baseline is inferred.",
            ],
            "summary": {
                "tasks": len(metrics),
                "states": dict(sorted(collections.Counter(t["state"] for t in metrics).items())),
                "events": sum(t["event_count"] for t in metrics),
                "tool_calls": sum(t["tool_calls"] for t in metrics),
                "done_with_unfinished_evidence": sum(t["done_with_unfinished_evidence"] for t in metrics),
                "tasks_with_plan": sum(t["has_plan"] for t in metrics),
            },
            "tasks": metrics,
        }
    finally:
        conn.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", required=True)
    parser.add_argument("--since", type=int, default=0)
    parser.add_argument("--until", type=int, default=2**63 - 1)
    args = parser.parse_args()
    if args.since >= args.until:
        parser.error("--since must be less than --until")
    print(json.dumps(collect(args.db, args.since, args.until), ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
