# SPDX-License-Identifier: AGPL-3.0-or-later
"""Minimal stdlib-only client for `panda rpc` (NDJSON over stdio).

Usage as a library:

    from panda_rpc import PandaRPC
    with PandaRPC() as p:
        for kind, payload in p.ask("hello", session_id=None):
            if kind == "event":
                print(" ", payload)
            elif kind == "result":
                print(payload["answer"])

Or standalone:

    python3 scripts/panda_rpc.py "your prompt" [--session ID] [--config PATH]

Wire format (one JSON object per line, either direction):

    request:  {"id": N, "method": "<name>", "params": {...}}
    event:    {"id": N, "event": "delta"|"status"|"reasoning", "data": {...}}
    result:   {"id": N, "result": {...}}   or   {"id": N, "error": {"message": ...}}

Requests are served one at a time — sequentialize calls on one process.
"""

import json
import subprocess
import sys


class PandaRPC:
    def __init__(self, panda_bin="panda", config=None, card=None):
        cmd = [panda_bin, "rpc"]
        if config:
            cmd += ["--config", config]
        if card:
            cmd += ["--card", card]
        self._proc = subprocess.Popen(
            cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True
        )
        self._next = 0

    def close(self):
        if self._proc.stdin:
            self._proc.stdin.close()
        self._proc.wait(timeout=10)

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.close()

    def _call(self, method, params=None):
        self._next += 1
        req = {"id": self._next, "method": method}
        if params:
            req["params"] = params
        self._proc.stdin.write(json.dumps(req) + "\n")
        self._proc.stdin.flush()
        while True:
            line = self._proc.stdout.readline()
            if not line:
                raise RuntimeError("panda rpc exited")
            msg = json.loads(line)
            if "event" in msg:
                yield ("event", {"name": msg["event"], "data": msg.get("data")})
            elif "error" in msg:
                yield ("error", msg["error"])
                return
            else:
                yield ("result", msg.get("result"))
                return

    def status(self):
        return list(self._call("status"))[-1][1]

    def ask(self, prompt, session_id=None, work_dir=None, authorize=False):
        params = {"prompt": prompt, "authorize": authorize}
        if session_id:
            params["session_id"] = session_id
        if work_dir:
            params["work_dir"] = work_dir
        return self._call("ask", params)

    def session_new(self, title="", project=""):
        return list(self._call("session.new", {"title": title, "project": project}))[-1][1]

    def session_list(self):
        return list(self._call("session.list"))[-1][1]

    def session_get(self, sid):
        return list(self._call("session.get", {"id": sid}))[-1][1]

    def session_fork(self, sid, at=0):
        return list(self._call("session.fork", {"id": sid, "at": at}))[-1][1]

    def convo_get(self):
        """Bare-mode (sessionless) conversation turns."""
        return list(self._call("convo.get"))[-1][1]


if __name__ == "__main__":
    import argparse

    ap = argparse.ArgumentParser()
    ap.add_argument("prompt")
    ap.add_argument("--session", default=None)
    ap.add_argument("--config", default=None)
    ap.add_argument("--panda", default="panda")
    args = ap.parse_args()

    with PandaRPC(panda_bin=args.panda, config=args.config) as p:
        for kind, payload in p.ask(args.prompt, session_id=args.session):
            if kind == "event":
                if payload["name"] == "delta":
                    print(payload["data"]["text"], end="", flush=True)
            elif kind == "error":
                print("\nerror:", payload["message"], file=sys.stderr)
                sys.exit(1)
            else:
                print()
                if payload.get("kind") == "task":
                    print("[task]", payload.get("task_id"), payload.get("task_state"))
