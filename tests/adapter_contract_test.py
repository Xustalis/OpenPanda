#!/usr/bin/env python3
"""Black-box command contracts for the bundled Agent adapters.

These tests never call a real provider. They put a deterministic fake CLI first
on PATH, run the actual adapter script, and assert the adapter's subprocess
argv, cwd, environment, progress stream, timeout result, and JSON reduction.
The HarnessContractTest cases drive the shared runtime (adapters/_harness.py)
directly: request parsing, the unified result envelope, exit-code passthrough,
and the timeout process-tree kill.
"""
import json
import os
import pathlib
import stat
import subprocess
import sys
import tempfile
import time
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
ADAPTERS = ROOT / "adapters"


def write_executable(path, body):
    path.write_text("#!/usr/bin/env python3\n" + body, encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IXUSR)


def run_adapter(name, cli_name, cli_body, env=None, timeout=5):
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)
        work = tmp / "work"
        work.mkdir()
        write_executable(tmp / cli_name, cli_body)
        merged = os.environ.copy()
        merged["PATH"] = str(tmp) + os.pathsep + merged.get("PATH", "")
        if env:
            merged.update(env)
        req = {"prompt": "contract prompt", "timeout_s": 2, "cwd": str(work)}
        proc = subprocess.run(
            [sys.executable, str(ADAPTERS / name)],
            input=json.dumps(req), text=True, capture_output=True,
            env=merged, cwd=str(ROOT), timeout=timeout,
        )
        lines = [line for line in proc.stderr.splitlines() if line.strip()]
        payload = json.loads(proc.stdout.strip())
        return payload, lines, tmp


def run_harness(body, stdin_data="", env=None, timeout=5):
    """Run a snippet against adapters/_harness.py directly and return
    (stdout payload, completed process)."""
    code = ("import sys; sys.path.insert(0, %r); import _harness; " % str(ADAPTERS)) + body
    merged = os.environ.copy()
    if env:
        merged.update(env)
    proc = subprocess.run(
        [sys.executable, "-c", code], input=stdin_data, text=True,
        capture_output=True, env=merged, cwd=str(ROOT), timeout=timeout,
    )
    payload = json.loads(proc.stdout.strip()) if proc.stdout.strip() else None
    return payload, proc


class AdapterContractTest(unittest.TestCase):
    def test_claude_stream_contract(self):
        payload, progress, _ = run_adapter(
            "claude_code.py", "claude", r'''
import json, os, sys
assert sys.argv[1:3] == ["-p", "contract prompt"]
assert "--output-format" in sys.argv and "stream-json" in sys.argv
assert "--verbose" in sys.argv
assert "--allowedTools" in sys.argv
assert "--max-turns" in sys.argv and "30" in sys.argv
assert os.getcwd().endswith("/work")
assert os.environ.get("ANTHROPIC_API_KEY") == "native-key"
print(json.dumps({"type":"assistant", "message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"pwd"}}]}}))
print(json.dumps({"type":"result", "is_error":False, "result":"claude answer", "usage":{"input_tokens":3,"output_tokens":5}, "total_cost_usd":0.25}))
''',
            env={"ANTHROPIC_API_KEY": "native-key", "CLAUDE_MODEL": "claude-test"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "claude answer")
        self.assertEqual(payload["tokens"], 8)
        self.assertEqual(payload["cost"], 0.25)
        self.assertTrue(any("Bash: pwd" in line for line in progress), progress)

    def test_codex_workspace_write_contract(self):
        payload, progress, _ = run_adapter(
            "codex.py", "codex", r'''
import json, os, sys
args = sys.argv[1:]
assert args[:2] == ["exec", "--json"]
assert "--skip-git-repo-check" in args
assert "--sandbox" in args and args[args.index("--sandbox") + 1] == "workspace-write"
assert "--ephemeral" in args
assert "-c" in args and 'approval_policy="never"' in args
assert "--model" in args and args[args.index("--model") + 1] == "codex-test"
assert args[-1] == "contract prompt"
assert os.environ.get("OPENAI_API_KEY") == "openai-key"
print(json.dumps({"type":"item.completed","item":{"type":"command_execution","command":"git status"}}))
print(json.dumps({"type":"item.completed","item":{"type":"agent_message","text":"codex answer"}}))
print(json.dumps({"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":6}}))
''',
            env={"OPENAI_API_KEY": "openai-key", "CODEX_MODEL": "codex-test", "ANTHROPIC_MODEL": "must-not-be-used"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "codex answer")
        self.assertEqual(payload["tokens"], 10)
        self.assertTrue(any("shell: git status" in line for line in progress), progress)

    def test_opencode_provider_model_contract(self):
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", r'''
import os, sys
args = sys.argv[1:]
assert args[:2] == ["run", "--print-logs=false"]
assert "--model" in args and args[args.index("--model") + 1] == "deepseek/deepseek-chat"
assert args[-1] == "contract prompt"
assert os.environ.get("OPENAI_API_KEY") == "openai-key"
print("opencode answer")
''',
            env={"OPENCODE_MODEL": "deepseek/deepseek-chat", "OPENAI_API_KEY": "openai-key"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "opencode answer")

    def test_codex_timeout_is_reported(self):
        payload, _, _ = run_adapter(
            "codex.py", "codex", r'''
import time
time.sleep(10)
''',
            timeout=5,
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 124)


class HarnessContractTest(unittest.TestCase):
    """Direct contracts for the shared runtime adapters/_harness.py."""

    def test_read_request_parses_contract(self):
        req = {"prompt": "hi there", "timeout_s": 7, "cwd": "/tmp"}
        payload, proc = run_harness(
            "p, t, c = _harness.read_request(); _harness.emit(True, p, t)",
            stdin_data=json.dumps(req),
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "hi there")
        self.assertEqual(payload["exit_code"], 7)
        self.assertEqual(proc.returncode, 0)

    def test_invalid_request_json_is_reported(self):
        payload, proc = run_harness(
            "_harness.read_request()", stdin_data="not json {{{")
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["result"], "invalid request JSON")
        self.assertEqual(payload["exit_code"], 2)
        self.assertEqual(proc.returncode, 0)

    def test_run_simple_passes_exit_code_through(self):
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            write_executable(tmp / "failcli", r'''
import sys
print("boom output")
sys.exit(3)
''')
            payload, _ = run_harness(
                "_harness.run_simple([%r])" % str(tmp / "failcli"))
            self.assertFalse(payload["ok"], payload)
            self.assertEqual(payload["exit_code"], 3)
            self.assertEqual(payload["result"], "boom output")

    def test_run_simple_falls_back_to_stderr_diagnosis(self):
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            write_executable(tmp / "errcli", r'''
import sys
sys.stderr.write("diagnosis text")
sys.exit(1)
''')
            payload, _ = run_harness(
                "_harness.run_simple([%r])" % str(tmp / "errcli"))
            self.assertFalse(payload["ok"], payload)
            self.assertEqual(payload["exit_code"], 1)
            self.assertEqual(payload["result"], "diagnosis text")

    def test_run_simple_missing_binary_is_reported(self):
        payload, _ = run_harness(
            "_harness.run_simple(['definitely-not-on-path-xyz'])")
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 127)
        self.assertEqual(payload["result"], "definitely-not-on-path-xyz binary not found")

    def test_timeout_kills_whole_process_tree(self):
        """The watchdog timeout must kill the CLI AND its children: the child
        keeps appending heartbeat lines while alive, so the tree kill is
        proven by the heartbeats stopping."""
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            heartbeat = tmp / "hb.log"
            write_executable(tmp / "treechild", r'''
import os, time
path = os.environ["TREE_HB"]
while True:
    with open(path, "a") as f:
        f.write("tick\n")
    time.sleep(0.1)
''')
            write_executable(tmp / "treecli", r'''
import os, subprocess, sys, time
subprocess.Popen([sys.executable, os.environ["TREE_CHILD"]])
time.sleep(60)
''')
            payload, _ = run_harness(
                "_harness.run_simple([%r], timeout=2, label='treecli')"
                % str(tmp / "treecli"),
                env={"TREE_CHILD": str(tmp / "treechild"),
                     "TREE_HB": str(heartbeat)},
                timeout=6,
            )
            self.assertFalse(payload["ok"], payload)
            self.assertEqual(payload["exit_code"], 124)
            self.assertEqual(payload["result"], "treecli timed out")

            def ticks():
                return len(heartbeat.read_text().splitlines()) if heartbeat.exists() else 0

            # The grandchild ran (heartbeats accumulated during the 2s run)…
            time.sleep(0.8)
            first = ticks()
            self.assertGreater(first, 0, "grandchild never started")
            # …and died with the tree instead of outliving the timeout.
            time.sleep(0.8)
            self.assertEqual(ticks(), first,
                             "grandchild survived the process-tree kill")


if __name__ == "__main__":
    unittest.main()
