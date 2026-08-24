#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import tempfile


def run(adapter, cwd):
    req = json.dumps({"prompt": "finish the scenario", "cwd": cwd})
    proc = subprocess.run([sys.executable, adapter], input=req, text=True, capture_output=True, check=True)
    return json.loads(proc.stdout), proc.stderr


def main():
    adapter = os.path.join(os.path.dirname(__file__), "long_task.py")
    with tempfile.TemporaryDirectory() as cwd:
        first, first_err = run(adapter, cwd)
        second, second_err = run(adapter, cwd)
    assert first["ok"] and "remains" in first["result"]
    assert second["ok"] and "verified" in second["result"]
    assert '"type": "progress"' in first_err
    assert '"type": "progress"' in second_err
    print("long-task scenario adapter: OK")


if __name__ == "__main__":
    main()
