#!/usr/bin/env python3
"""Black-box command contracts for the bundled Agent adapters.

These tests never call a real provider. They put a deterministic fake CLI first
on PATH, run the actual adapter script, and assert the adapter's subprocess
argv, cwd, environment, progress stream, timeout result, and JSON reduction.
"""
import json
import os
import pathlib
import stat
import subprocess
import sys
import tempfile
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


if __name__ == "__main__":
    unittest.main()
