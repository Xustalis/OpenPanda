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


def run_adapter(name, cli_name, cli_body, env=None, timeout=5, extra_request=None):
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
        if extra_request:
            req.update(extra_request)
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
print(json.dumps({"type":"result", "is_error":False, "result":"claude answer", "session_id":"sess-123", "usage":{"input_tokens":3,"output_tokens":5,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}, "total_cost_usd":0.25}))
''',
            env={"ANTHROPIC_API_KEY": "native-key", "CLAUDE_MODEL": "claude-test"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "claude answer")
        self.assertEqual(payload["tokens"], 8)
        self.assertEqual(payload["cost"], 0.25)
        # The structured breakdown and the resumable session ride the wire.
        self.assertEqual(payload["session_id"], "sess-123")
        self.assertEqual(payload["usage"], {
            "input_tokens": 3, "output_tokens": 5,
            "cache_read_tokens": 2, "cache_write_tokens": 1,
        })
        self.assertTrue(any("Bash: pwd" in line for line in progress), progress)

    def test_claude_resume_and_extended_policy_contract(self):
        payload, _, _ = run_adapter(
            "claude_code.py", "claude", r'''
import json, sys
args = sys.argv[1:]
# A follow-up round resumes the previous session.
assert "--resume" in args and args[args.index("--resume") + 1] == "sess-prev"
# tools_policy=extended lifts the whitelist entirely.
assert "--allowedTools" not in args
print(json.dumps({"type":"result", "is_error":False, "result":"resumed",
                  "session_id":"sess-next", "usage":{"input_tokens":1,"output_tokens":1}}))
''',
            extra_request={"resume": "sess-prev", "tools_policy": "extended"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "resumed")
        self.assertEqual(payload["session_id"], "sess-next")

    def test_claude_minimal_policy_keeps_whitelist(self):
        payload, _, _ = run_adapter(
            "claude_code.py", "claude", r'''
import json, sys
args = sys.argv[1:]
# minimal (and unset) runs keep the safe whitelist and never resume.
assert "--allowedTools" in args
assert "--resume" not in args
print(json.dumps({"type":"result", "is_error":False, "result":"ok",
                  "usage":{"input_tokens":1,"output_tokens":1}}))
''',
            extra_request={"tools_policy": "minimal"},
        )
        self.assertTrue(payload["ok"], payload)

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
print(json.dumps({"type":"session.created","payload":{"id":"codex-sess-1"}}))
print(json.dumps({"type":"item.completed","item":{"type":"command_execution","command":"git status"}}))
print(json.dumps({"type":"item.completed","item":{"type":"agent_message","text":"codex answer"}}))
print(json.dumps({"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":6}}))
''',
            env={"OPENAI_API_KEY": "openai-key", "CODEX_MODEL": "codex-test", "ANTHROPIC_MODEL": "must-not-be-used"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "codex answer")
        self.assertEqual(payload["tokens"], 10)
        # codex reports the breakdown too (no cost: the CLI vends none).
        self.assertEqual(payload["usage"], {"input_tokens": 4, "output_tokens": 6})
        self.assertNotIn("cost", payload)
        # The session id from the session envelope rides the wire for resume.
        self.assertEqual(payload["session_id"], "codex-sess-1")
        self.assertTrue(any("shell: git status" in line for line in progress), progress)

    def test_codex_resume_and_extended_policy_contract(self):
        payload, _, _ = run_adapter(
            "codex.py", "codex", r'''
import json, sys
args = sys.argv[1:]
# A follow-up round resumes the previous session by id.
assert args[:3] == ["exec", "resume", "codex-sess-1"], args
# The resumed run keeps its history: no --ephemeral.
assert "--ephemeral" not in args
# tools_policy=extended escalates the sandbox scope.
assert args[args.index("--sandbox") + 1] == "danger-full-access"
assert args[-1] == "contract prompt"
print(json.dumps({"type":"item.completed","item":{"type":"agent_message","text":"resumed"}}))
''',
            extra_request={"resume": "codex-sess-1", "tools_policy": "extended"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "resumed")

    def test_opencode_provider_model_contract(self):
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", r'''
import json, os, sys
args = sys.argv[1:]
assert args[:2] == ["run", "--print-logs=false"]
assert "--format" in args and args[args.index("--format") + 1] == "json"
assert "--model" in args and args[args.index("--model") + 1] == "deepseek/deepseek-chat"
assert args[-1] == "contract prompt"
assert os.environ.get("OPENAI_API_KEY") == "openai-key"
print(json.dumps({"type":"session.created","properties":{"info":{"id":"ses_oc1"}}}))
print(json.dumps({"type":"message.part.updated","properties":{"sessionID":"ses_oc1","part":{"id":"p1","type":"text","text":"opencode answer"}}}))
print(json.dumps({"type":"message.updated","properties":{"info":{"tokens":{"input":5,"output":7},"cost":0.02}}}))
''',
            env={"OPENCODE_MODEL": "deepseek/deepseek-chat", "OPENAI_API_KEY": "openai-key"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "opencode answer")
        # The event stream yields the session id and the usage breakdown.
        self.assertEqual(payload["session_id"], "ses_oc1")
        self.assertEqual(payload["usage"], {"input_tokens": 5, "output_tokens": 7})
        self.assertEqual(payload["tokens"], 12)
        self.assertEqual(payload["cost"], 0.02)

    def test_opencode_resume_and_plain_fallback_contract(self):
        # Resume threads --session through to the CLI.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", r'''
import json, sys
args = sys.argv[1:]
assert "--session" in args and args[args.index("--session") + 1] == "ses-prev"
print(json.dumps({"type":"message.part.updated","properties":{"part":{"id":"p","type":"text","text":"ok"}}}))
''',
            extra_request={"resume": "ses-prev"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok")
        # A CLI that rejects --format json degrades to the plain text run.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", r'''
import sys
args = sys.argv[1:]
if "--format" in args:
    sys.stderr.write("error: unknown option --format\n")
    sys.exit(1)
assert args[:2] == ["run", "--print-logs=false"]
print("plain opencode answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "plain opencode answer")

    def test_grok_session_contract(self):
        # A first run names its own session and returns it as session_id.
        payload, _, _ = run_adapter(
            "grok_build.py", "grok", r'''
import json, sys
args = sys.argv[1:]
assert "-s" in args and args[args.index("-s") + 1].startswith("panda-")
assert "--single" in args and "contract prompt" in args
assert "--output-format" in args and "plain" in args
assert "--always-approve" in args
print(json.dumps({"session": args[args.index("-s") + 1]}))
print("grok answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertIn("grok answer", payload["result"])
        self.assertTrue(payload.get("session_id", "").startswith("panda-"), payload)
        # A follow-up round resumes that named session via -s.
        payload, _, _ = run_adapter(
            "grok_build.py", "grok", r'''
import sys
args = sys.argv[1:]
assert args[args.index("-s") + 1] == "panda-prev"
print("grok resumed")
''',
            extra_request={"resume": "panda-prev"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["session_id"], "panda-prev")
        # A CLI without -s degrades to a nameless one-shot run.
        payload, _, _ = run_adapter(
            "grok_build.py", "grok", r'''
import sys
args = sys.argv[1:]
if "-s" in args:
    sys.stderr.write("error: unrecognized option '-s'\n")
    sys.exit(1)
print("grok legacy answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "grok legacy answer")
        self.assertNotIn("session_id", payload)

    def test_plain_adapters_keep_their_cli_contract(self):
        # dsh / hermes / openclaw stay thin passthroughs: pin each command
        # line so a future change is a deliberate contract edit.
        payload, _, _ = run_adapter(
            "deepseek_harness.py", "dsh", r'''
import sys
assert sys.argv[1:] == ["--profile", "headless", "contract prompt"]
print("dsh answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "dsh answer")
        payload, _, _ = run_adapter(
            "hermes.py", "hermes", r'''
import sys
assert sys.argv[1:] == ["--cli", "--yolo", "-z", "contract prompt"]
print("hermes answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "hermes answer")
        payload, _, _ = run_adapter(
            "openclaw.py", "openclaw", r'''
import sys
assert sys.argv[1:] == ["agent", "exec", "contract prompt"]
print("openclaw answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "openclaw answer")

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

    def test_read_request_carries_resume_and_policy(self):
        req = {"prompt": "p", "resume": "sess-9", "tools_policy": "extended"}
        payload, proc = run_harness(
            "r = _harness.read_request(); "
            "_harness.emit(True, r.resume + ':' + r.tools_policy, 0)",
            stdin_data=json.dumps(req),
        )
        self.assertEqual(payload["result"], "sess-9:extended", payload)

    def test_emit_carries_usage_and_session(self):
        payload, _ = run_harness(
            "_harness.emit(True, 'done', 0, tokens=7, "
            "usage={'input_tokens': 3, 'output_tokens': 4}, "
            "session_id='sess-1')",
        )
        self.assertEqual(payload["tokens"], 7)
        self.assertEqual(payload["usage"], {"input_tokens": 3, "output_tokens": 4})
        self.assertEqual(payload["session_id"], "sess-1")
        # An all-zero usage block is noise and stays off the wire.
        payload, _ = run_harness(
            "_harness.emit(True, 'done', 0, usage={'input_tokens': 0})")
        self.assertNotIn("usage", payload)
        self.assertNotIn("session_id", payload)

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
