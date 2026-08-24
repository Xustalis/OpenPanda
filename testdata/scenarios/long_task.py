#!/usr/bin/env python3
"""Deterministic adapter for long-running supervision scenarios."""
import json
import os
import sys


def emit(ok, result, code=0):
    print(json.dumps({"ok": ok, "result": result, "exit_code": code}))


def main():
    try:
        req = json.loads(sys.stdin.read() or "{}")
    except json.JSONDecodeError:
        emit(False, "invalid request", 2)
        return
    cwd = req.get("cwd") or "."
    state_path = os.path.join(cwd, ".openpanda-scenario-state.json")
    try:
        with open(state_path, "r", encoding="utf-8") as f:
            state = json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        state = {"round": 0}
    state["round"] = int(state.get("round", 0)) + 1
    with open(state_path, "w", encoding="utf-8") as f:
        json.dump(state, f)
    if state["round"] == 1:
        print(json.dumps({"type": "progress", "note": "completed discovery; implementation remains"}), file=sys.stderr, flush=True)
        emit(True, "discovery complete; implementation remains")
        return
    print(json.dumps({"type": "progress", "note": "implemented and verified all required steps"}), file=sys.stderr, flush=True)
    emit(True, "implemented and verified all required steps")


if __name__ == "__main__":
    main()
