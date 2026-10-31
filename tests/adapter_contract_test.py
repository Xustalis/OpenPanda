#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
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
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
from unittest import mock


ROOT = pathlib.Path(__file__).resolve().parents[1]
ADAPTERS = ROOT / "adapters"


def write_executable(path, body):
    """Lay down a runnable fake CLI at `path`.

    POSIX hands the shebang script straight to the kernel. Windows cannot:
    CreateProcess ignores a shebang and the filesystem has no exec bit, so the
    runnable file is a two-line .cmd launcher naming the interpreter and the
    script. _harness._shim_exe resolves that pair to [interpreter, script], so
    the fake still receives the adapter's argv verbatim instead of going
    through cmd.exe's %* requoting — the same fidelity the POSIX script gets.
    """
    if sys.platform == "win32":
        script = path.with_name(path.name + ".py")
        script.write_bytes(("#!" + sys.executable + "\n" + body).encode("utf-8"))
        shim = path.with_name(path.name + ".cmd")
        shim.write_bytes(
            ('@echo off\r\n"%s" "%%~dp0%s" %%*\r\n'
             % (sys.executable, script.name)).encode("utf-8"))
        return shim
    path.write_text("#!/usr/bin/env python3\n" + body, encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IXUSR)
    return path


def source_path(path):
    """The Python source backing a fake CLI, for callers that run it through
    an interpreter rather than through the adapter's PATH resolution."""
    return path.with_name(path.name + ".py") if sys.platform == "win32" else path


def home_env(home):
    """Point every platform's `~` at one fixture directory.

    posixpath.expanduser reads HOME, ntpath reads USERPROFILE — so a case that
    plants a credential under a temp home only reaches its own file on POSIX.
    On windows-latest the adapter looked in the runner's profile instead, found
    no opencode login, dropped the Go preference and ran without `--model`,
    which read as a preference bug rather than a fixture that only spoke POSIX.
    Both names, one directory; the unused one is inert on each platform.
    """
    return {"HOME": str(home), "USERPROFILE": str(home)}


def run_adapter(name, cli_name, cli_body, env=None, timeout=10, extra_request=None):
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)
        work = tmp / "work"
        work.mkdir()
        write_executable(tmp / cli_name, cli_body)
        merged = os.environ.copy()
        # Adapter contracts must not inherit the developer machine's model
        # routing. Individual cases opt into overrides through env below.
        for key in (
            "OPENCODE_MODEL", "ANTHROPIC_MODEL", "OPENAI_MODEL",
            "OPENPANDA_INJECTED_MODEL", "OPENPANDA_ADAPTER_CACHE",
            "OPENPANDA_CACHE_DIR",
            # Ambient provider endpoints would otherwise leak into the
            # adapter's injection detection (a developer shell inside
            # Claude Code exports ANTHROPIC_BASE_URL).
            "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY",
            "OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENCODE_API_KEY",
            "OPENPANDA_MODEL_BASE_URL", "OPENPANDA_MODEL_API_KEY",
            "OPENPANDA_MODEL_NAME", "OPENPANDA_MODEL_API_TYPE",
            "OPENPANDA_AGENT_MODELS",
            # Ambient homes and provider settings the new injected-run cases
            # read. A developer shell that exports any of these would make
            # "a plain run leaves the operator's config alone" fail spuriously.
            "HERMES_HOME",
            "ZCODE_API_KEY", "ZCODE_BASE_URL", "ZCODE_MODEL",
            "ZCODE_PERSONAL_PROVIDER_CONFIG_FILE",
            "PI_API_KEY", "PI_MODEL", "PI_PROVIDER", "PI_CODING_AGENT_DIR",
            "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL",
            "GROK_MODEL", "GROK_MODELS_BASE_URL", "GROK_BASE_URL",
            "GROK_API_KEY",
            "OPENCLAW_API_KEY", "OPENCLAW_MODEL", "OPENCLAW_CONFIG",
            "OPENCLAW_STATE_DIR",
            # Windows locations for opencode's state files must not leak a
            # developer machine's config into the fixture.
            "APPDATA", "LOCALAPPDATA",
        ):
            merged.pop(key, None)
        # Probes must answer from the fake CLI in every case, never from a
        # developer machine's cache (one test turns the cache back on).
        merged["OPENPANDA_ADAPTER_CACHE"] = "0"
        merged["PATH"] = str(tmp) + os.pathsep + merged.get("PATH", "")
        if env:
            merged.update(env)
        # timeout_s is the adapter's watchdog budget for the fake CLI: 3s was
        # tight enough that suite-level load (parallel jobs on a cold box)
        # could push a trivial fake past it and flake — 30s leaves the
        # watchdog exercised (the dedicated timeout tests override it) without
        # racing scheduler jitter.
        req = {"prompt": "contract prompt", "timeout_s": 30, "cwd": str(work)}
        if extra_request:
            req.update(extra_request)
        proc = subprocess.run(
            [sys.executable, str(ADAPTERS / name)],
            input=json.dumps(req), text=True, encoding="utf-8", errors="replace",
            capture_output=True,
            env=merged, cwd=str(ROOT), timeout=timeout,
        )
        lines = [line for line in proc.stderr.splitlines() if line.strip()]
        payload = json.loads(proc.stdout.strip())
        return payload, lines, tmp


# Opencode's adapter probes `opencode run --help` once per process for the
# flags the installed build accepts (they drift: --dangerously-skip-permissions
# became --auto; --format json arrived later). These preambles answer that
# probe inside the fake CLIs — current-build flags, and the legacy set.
OC_HELP_CURRENT = r'''
import sys
if "--help" in sys.argv:
    print("opencode run [message..]")
    print("      --format  format: default (formatted) or json (raw JSON events)")
    print("      --auto    auto-approve permissions that are not explicitly denied")
    print("      --agent   agent to use")
    print("      --standalone  run with a private server instead of the background service")
    sys.exit(0)
'''

OC_HELP_LEGACY = r'''
import sys
if "--help" in sys.argv:
    print("opencode run [message..]")
    print("      --dangerously-skip-permissions  auto-approve permissions (dangerous!)")
    sys.exit(0)
'''


def run_harness(body, stdin_data="", env=None, timeout=5):
    """Run a snippet against adapters/_harness.py directly and return
    (stdout payload, completed process)."""
    code = ("import sys; sys.path.insert(0, %r); import _harness; " % str(ADAPTERS)) + body
    merged = os.environ.copy()
    if env:
        merged.update(env)
    proc = subprocess.run(
        [sys.executable, "-c", code], input=stdin_data, text=True,
        encoding="utf-8", errors="replace",
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
assert os.path.basename(os.getcwd()) == "work"
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

    def test_claude_timeout_reports_session_checkpoint(self):
        """A watchdog-killed stream run reports the session id the init
        envelope carried, so the orchestrator's retry resumes the same
        conversation instead of cold-starting."""
        payload, _, _ = run_adapter(
            "claude_code.py", "claude", r'''
import json, sys, time
print(json.dumps({"type":"system","subtype":"init","session_id":"sess-t9"}))
sys.stdout.flush()
time.sleep(10)
''',
            extra_request={"timeout_s": 2},
            timeout=8,
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 124)
        self.assertEqual(payload["session_id"], "sess-t9")

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

    def test_codex_transcript_events(self):
        """Codex items land on the transcript: a tool-shaped item is a
        tool_use on item.started and a tool_result on item.completed;
        agent_message is text, reasoning is thinking."""
        payload, lines, _ = run_adapter(
            "codex.py", "codex", r'''
import json
print(json.dumps({"type":"session_meta","payload":{"id":"cs-1"}}))
print(json.dumps({"type":"item.started","item":{"id":"i1","type":"command_execution","command":"ls -la","status":"in_progress"}}))
print(json.dumps({"type":"item.completed","item":{"id":"i2","type":"reasoning","text":"pondering"}}))
print(json.dumps({"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"ls -la","status":"completed","aggregated_output":"total 0"}}))
print(json.dumps({"type":"item.completed","item":{"id":"i3","type":"agent_message","text":"codex answer"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        events = [json.loads(l) for l in lines if l.startswith("{")]
        events = [e for e in events if e.get("type") == "event"]
        kinds = [e["ev"] for e in events]
        # "session" announces the minted conversation id the moment it is
        # known — Go latches it so a killed run can resume — ahead of the
        # transcript items.
        self.assertEqual(kinds, ["session", "tool_use", "thinking", "tool_result", "text"])
        self.assertEqual(events[0]["id"], "cs-1")
        self.assertEqual(events[1]["name"], "shell")
        self.assertEqual(events[1]["input"], {"command": "ls -la"})
        self.assertEqual(events[1]["id"], "i1")
        self.assertEqual(events[2]["thinking"], "pondering")
        self.assertEqual(events[3]["tool_use_id"], "i1")
        self.assertEqual(events[3]["content"], "total 0")
        self.assertFalse(events[3]["is_error"])
        self.assertEqual(events[4]["text"], "codex answer")
        self.assertEqual(events[4]["id"], "i3")

    def test_codex_completion_only_tool_pair(self):
        """Older codex streams emit item.completed with no item.started:
        the adapter synthesizes the missing tool_use so the transcript never
        shows an orphaned result."""
        payload, lines, _ = run_adapter(
            "codex.py", "codex", r'''
import json
print(json.dumps({"type":"item.completed","item":{"id":"w1","type":"command_execution","command":"pwd","status":"completed","aggregated_output":"/repo"}}))
print(json.dumps({"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"done"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        events = [json.loads(l) for l in lines if l.startswith("{")]
        events = [e for e in events if e.get("type") == "event"]
        kinds = [e["ev"] for e in events]
        # The synthesized use+result pair precedes the message text.
        self.assertEqual(kinds, ["tool_use", "tool_result", "text"])
        self.assertEqual(events[0]["id"], "w1")
        self.assertEqual(events[0]["name"], "shell")
        self.assertEqual(events[1]["tool_use_id"], "w1")
        self.assertEqual(events[1]["content"], "/repo")

    def test_opencode_transcript_events(self):
        """Opencode parts land on the transcript once: text on first
        sighting, tool_use on first sighting, tool_result on the first
        terminal status — re-emitted parts never duplicate a row."""
        payload, lines, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json
print(json.dumps({"type":"tool_use","sessionID":"s1","part":{"id":"pt","type":"tool","tool":"bash","state":{"status":"running","input":{"command":"uname"},"title":"uname"}}}))
print(json.dumps({"type":"tool_use","sessionID":"s1","part":{"id":"pt","type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"uname"},"output":"arm64","title":"uname"}}}))
# A status re-emit must not duplicate the tool_result row.
print(json.dumps({"type":"tool_use","sessionID":"s1","part":{"id":"pt","type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"uname"},"output":"arm64","title":"uname"}}}))
print(json.dumps({"type":"text","sessionID":"s1","part":{"id":"px","type":"text","text":"half"}}))
print(json.dumps({"type":"text","sessionID":"s1","part":{"id":"px","type":"text","text":"half plus more"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        events = [json.loads(l) for l in lines if l.startswith("{")]
        events = [e for e in events if e.get("type") == "event"]
        kinds = [e["ev"] for e in events]
        self.assertEqual(kinds, ["tool_use", "tool_result", "text"])
        self.assertEqual(events[0]["name"], "bash")
        self.assertEqual(events[0]["input"], {"command": "uname"})
        self.assertEqual(events[1]["tool_use_id"], "pt")
        self.assertEqual(events[1]["content"], "arm64")
        self.assertFalse(events[1]["is_error"])
        # First sighting wins — the update does not re-emit a second row.
        self.assertEqual(events[2]["text"], "half")

    def test_opencode_probe_cache_across_processes(self):
        """Roster, catalog and flag probes survive between adapter processes.

        Adapters are one process per task; before the cross-process cache a
        burst of opencode tasks spawned `opencode models`, `api model.list`
        and `run --help` every single time. Two runs sharing one cache must
        pay each probe once, and the run itself must still work."""
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            work = tmp / "work"
            work.mkdir()
            hits = tmp / "probe-hits"
            hits_repr = repr(str(hits))
            body = (
                "import json, sys\n"
                "if '--help' in sys.argv:\n"
                f"    open({hits_repr}, 'a').write('help\\n')\n"
                "    print('opencode run [message..]')\n"
                "    print('      --format  format: default (formatted) or json (raw JSON events)')\n"
                "    print('      --auto    auto-approve permissions')\n"
                "    print('      --agent   agent to use')\n"
                "    sys.exit(0)\n"
                "if sys.argv[1:2] == ['models']:\n"
                f"    open({hits_repr}, 'a').write('models\\n')\n"
                "    print('opencode/deepseek-v4-flash-free')\n"
                "    sys.exit(0)\n"
                "if sys.argv[1:3] == ['api', 'model.list']:\n"
                f"    open({hits_repr}, 'a').write('catalog\\n')\n"
                "    print(json.dumps({'data': [{'providerID': 'opencode', 'id': 'deepseek-v4-flash-free', 'enabled': True, 'status': 'active', 'capabilities': {'tools': True}}]}))\n"
                "    sys.exit(0)\n"
                "print(json.dumps({'type': 'text', 'sessionID': 's1',"
                " 'part': {'id': 'p1', 'type': 'text', 'text': 'cached run'}}))\n"
            )
            write_executable(tmp / "opencode", body)

            env = os.environ.copy()
            for key in ("OPENCODE_MODEL", "ANTHROPIC_MODEL", "OPENAI_MODEL",
                        "OPENPANDA_INJECTED_MODEL", "OPENPANDA_CACHE_DIR"):
                env.pop(key, None)
            env["PATH"] = str(tmp) + os.pathsep + env.get("PATH", "")
            # No ambient opencode config in the identity, on either platform.
            env.update(home_env(tmp))
            env["XDG_CACHE_HOME"] = str(tmp / "cache")
            # cache_dir() reads LOCALAPPDATA on win32, not XDG_CACHE_HOME: without
            # this the test writes its fixtures into the developer's real cache.
            env["LOCALAPPDATA"] = str(tmp / "localappdata")
            env["APPDATA"] = str(tmp / "appdata")
            env["OPENPANDA_ADAPTER_CACHE"] = "1"
            req = {"prompt": "contract prompt", "timeout_s": 30, "cwd": str(work)}

            for _ in range(2):
                proc = subprocess.run(
                    [sys.executable, str(ADAPTERS / "opencode.py")],
                    input=json.dumps(req), text=True, capture_output=True,
                    env=env, cwd=str(ROOT), timeout=30, encoding="utf-8",
                )
                payload = json.loads(proc.stdout.strip())
                self.assertTrue(payload["ok"], payload)
                self.assertEqual(payload["result"], "cached run")

            lines = hits.read_text(encoding="utf-8").splitlines()
            self.assertEqual(lines.count("help"), 1, lines)
            self.assertEqual(lines.count("models"), 1, lines)
            self.assertEqual(lines.count("catalog"), 1, lines)

    def test_claude_injected_model_strips_settings_on_stream_only(self):
        # Credential rescue: an injected model/base URL disables the user's
        # settings sources on the stream path…
        payload, _, _ = run_adapter(
            "claude_code.py", "claude", r'''
import json, sys
args = sys.argv[1:]
assert "--setting-sources" in args
assert args[args.index("--setting-sources") + 1] == ""
print(json.dumps({"type":"result", "is_error":False, "result":"rescued",
                  "session_id":"sess-x", "usage":{"input_tokens":1,"output_tokens":1}}))
''',
            env={"OPENPANDA_INJECTED_MODEL": "1"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "rescued")
        # …but the plain-mode degrade must not resend the flag an old CLI
        # just rejected — that run would hard-fail with no fallback.
        payload, _, _ = run_adapter(
            "claude_code.py", "claude", r'''
import json, sys
args = sys.argv[1:]
if "stream-json" in args:
    sys.stderr.write("error: unknown option --setting-sources\n")
    sys.exit(1)
assert "--setting-sources" not in args, args
print(json.dumps({"is_error": False, "result": "degraded"}))
''',
            env={"OPENPANDA_INJECTED_MODEL": "1"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "degraded")

    def test_opencode_provider_model_contract(self):
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, os, sys
args = sys.argv[1:]
assert args[:2] == ["run", "--print-logs=true"]
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

    # The roster `opencode models` serves on this fake install — the probe the
    # adapter runs before passing --model, because free tiers get renamed.
    OC_MODELS = r'''
import sys
if sys.argv[1:2] == ["models"]:
    print("opencode/deepseek-v4.1-flash-free")
    print("opencode/foo-v4-flash-free")
    print("opencode/ll-alpha-free")
    sys.exit(0)
'''

    def test_opencode_model_roster_substitutes_stale_request(self):
        """A requested model the install no longer serves is re-anchored to the
        closest live one — the stale id used to make every run die in seconds
        with a yargs resolution error, which then read as a task failure."""
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + self.OC_MODELS + r'''
import json, sys
args = sys.argv[1:]
assert "--model" in args and args[args.index("--model") + 1] == "opencode/foo-v4-flash-free", args
print(json.dumps({"type":"text","sessionID":"ses_m1","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={"OPENPANDA_INJECTED_MODEL": "1", "OPENCODE_MODEL": "openai/foo-flash"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok")
        # The swap is visible: the progress stream names both ids.
        self.assertTrue(
            any("openai/foo-flash is not available; using opencode/foo-v4-flash-free" in line
                for line in progress),
            progress,
        )

    def test_opencode_model_roster_fails_fast_when_nothing_matches(self):
        """No served model matches the request: fail with the roster instead of
        handing the CLI a model it will reject opaquely."""
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + self.OC_MODELS + r'''
import sys
raise SystemExit("the run must never start on an unresolvable model")
''',
            env={"OPENPANDA_INJECTED_MODEL": "1", "OPENCODE_MODEL": "openai/zzz-unicorn"},
        )
        self.assertFalse(payload["ok"], payload)
        self.assertIn("available models", payload["result"])
        self.assertIn("opencode/foo-v4-flash-free", payload["result"])

    def test_opencode_model_roster_probe_failure_keeps_passthrough(self):
        """A build without `opencode models` (probe fails) keeps the old
        contract: the requested model passes through untouched."""
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
args = sys.argv[1:]
assert args[:2] == ["run", "--print-logs=true"], args
assert "--model" in args and args[args.index("--model") + 1] == "deepseek/deepseek-chat", args
print(json.dumps({"type":"text","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={"OPENCODE_MODEL": "deepseek/deepseek-chat"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok")

    # The metadata catalog `opencode api model.list` serves on a v2 install —
    # the capability data behind the auto pick: context/output limits, tool
    # support, modalities, reasoning variants and cost.
    OC_API_MODELS = r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "deepseek-v4.1-flash-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 200000, "output": 32000},
         "status": "active", "enabled": True,
         "variants": [{"id": "low"}, {"id": "high"}]},
        {"id": "foo-v4-flash-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 200000, "output": 32000},
         "status": "active", "enabled": True},
        {"id": "ll-alpha-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 262144, "output": 65536},
         "status": "active", "enabled": True,
         "variants": [{"id": "low"}, {"id": "high"}]},
        {"id": "nemotron-3.5-super-vl-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text", "image"]},
         "limit": {"context": 1048576, "output": 131072},
         "status": "active", "enabled": True,
         "variants": [{"id": "low"}, {"id": "medium"}, {"id": "high"}]},
        {"id": "tooless-free", "providerID": "opencode",
         "capabilities": {"tools": False, "input": ["text"]},
         "limit": {"context": 1048576, "output": 131072},
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
'''

    # The pick roster: a 1M-context flagship, a mid flash, a marked alpha and
    # a tool-less chat model the pick must never take for agent work.
    OC_PICK_ROSTER = r'''
import sys
if sys.argv[1:2] == ["models"]:
    print("opencode/deepseek-v4.1-flash-free")
    print("opencode/foo-v4-flash-free")
    print("opencode/ll-alpha-free")
    print("opencode/nemotron-3.5-super-vl-free")
    print("opencode/tooless-free")
    sys.exit(0)
'''

    def test_opencode_auto_picks_most_capable_catalog_model(self):
        """No env pin: every run re-reads the roster and picks by capability —
        the 1M-context flagship with reasoning variants beats the flash tier
        and the alpha, and the tool-less model never qualifies."""
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode",
            OC_HELP_CURRENT + self.OC_PICK_ROSTER + self.OC_API_MODELS + r'''
import json, sys
args = sys.argv[1:]
assert "--model" in args, args
assert args[args.index("--model") + 1] == "opencode/nemotron-3.5-super-vl-free", args
print(json.dumps({"type":"text","sessionID":"ses_p1","part":{"id":"px","type":"text","text":"ok"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertTrue(
            any("auto-selected model opencode/nemotron-3.5-super-vl-free" in line
                for line in progress),
            progress,
        )

    def test_opencode_auto_pick_slug_heuristic_without_catalog(self):
        """A build whose `api` subcommand fails still picks by slug heuristics:
        the coding-family model outranks the small/alpha entries."""
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import sys
if sys.argv[1:2] == ["models"]:
    print("opencode/mimo-v2.6-flash-free")
    print("opencode/ll-alpha-free")
    print("opencode/qwen3-coder-free")
    sys.exit(0)
if sys.argv[1:3] == ["api", "model.list"]:
    sys.stderr.write("Operation not found: model.list\n")
    sys.exit(1)
''' + r'''
import json, sys
args = sys.argv[1:]
assert "--model" in args, args
assert args[args.index("--model") + 1] == "opencode/qwen3-coder-free", args
print(json.dumps({"type":"text","sessionID":"ses_p2","part":{"id":"px","type":"text","text":"ok"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)

    def test_opencode_catalog_is_the_roster_when_models_is_empty(self):
        """v2 builds answer `opencode models` with an empty list. The catalog
        probe must then be the roster: an unpinned run still picks a model and
        identifies what is served, instead of passing no --model at all."""
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:2] == ["models"]:
    sys.exit(0)  # v2: empty roster
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "deepseek-v4.1-flash-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 200000, "output": 32000},
         "cost": [{"input": 0, "output": 0}],
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/deepseek-v4.1-flash-free", args
print(json.dumps({"type":"text","sessionID":"ses_c1","part":{"id":"px","type":"text","text":"ok"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok")
        self.assertTrue(
            any("identified 1 model served by this install" in line
                for line in progress),
            progress,
        )

    def test_opencode_billing_failure_falls_back_to_free_model(self):
        """A paid model the account cannot pay for is not a roster rotation:
        the adapter retries the same task on the best free model instead of
        ending the run with the gateway's billing error."""
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode",
            OC_HELP_CURRENT + self.OC_PAID_ROSTER + self.OC_PAID_API + r'''
import json, sys
args = sys.argv[1:]
model = args[args.index("--model") + 1]
if model == "anthropic/claude-4.6-opus":
    print(json.dumps({"type": "error", "error": {
        "type": "api_error",
        "message": "Upstream request failed: Insufficient account funds"}}))
    sys.exit(0)
print(json.dumps({"type":"text","sessionID":"ses_b1","part":{"id":"px","type":"text","text":"ok on " + model}}))
''',
            extra_request={"complexity": 0.9},
            env={"OPENPANDA_OPENCODE_ALLOW_PAID": "1"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok on opencode/nemotron-3.5-super-vl-free")
        self.assertTrue(
            any("rejected for billing/quota; retrying on free model" in line
                for line in progress),
            progress,
        )

    def test_opencode_go_models_need_a_credential(self):
        """The subscription roster without a stored login cannot serve:
        its models are disqualified so the pick lands on the free Zen tier
        instead of dying on "Go requires a credential"."""
        home = tempfile.mkdtemp()
        cache = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, home, True)
        self.addCleanup(shutil.rmtree, cache, True)
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "iceman-contributor-free", "providerID": "opencode-go",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 262144, "output": 131072},
         "cost": [{"input": 0, "output": 0}],
         "status": "active", "enabled": True},
        {"id": "deepseek-v4.1-flash-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 200000, "output": 32000},
         "cost": [{"input": 0, "output": 0}],
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/deepseek-v4.1-flash-free", args
print(json.dumps({"type":"text","sessionID":"ses_g1","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={**home_env(home), "XDG_CACHE_HOME": cache},
        )
        self.assertTrue(payload["ok"], payload)

    def test_opencode_model_preferences_choose_in_order(self):
        """routing.agent_models is an ordered shortlist: the first entry the
        install serves and can authenticate wins, even a paid model. An entry
        that cannot run (here, a Go model with no login) is skipped with a
        progress note instead of silently replacing the list."""
        home = tempfile.mkdtemp()
        cache = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, home, True)
        self.addCleanup(shutil.rmtree, cache, True)
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "kimi-k3", "providerID": "opencode-go",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1048576, "output": 131072},
         "cost": [{"input": 3, "output": 15}],
         "status": "active", "enabled": True},
        {"id": "glm-5.3-flash", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1000000, "output": 131072},
         "cost": [{"input": 0.15, "output": 0.5}],
         "status": "active", "enabled": True},
        {"id": "deepseek-v4.1-flash-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 200000, "output": 32000},
         "cost": [{"input": 0, "output": 0}],
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/glm-5.3-flash", args
print(json.dumps({"type":"text","sessionID":"ses_pf1","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={
                **home_env(home),
                "XDG_CACHE_HOME": cache,
                "OPENPANDA_AGENT_MODELS": json.dumps(
                    ["opencode-go/kimi-k3", "opencode/glm-5.3-flash"]),
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok")
        self.assertTrue(
            any("needs an OpenCode Go credential" in line for line in progress),
            progress,
        )
        self.assertTrue(
            any("using preferred model opencode/glm-5.3-flash" in line
                for line in progress),
            progress,
        )

    def test_opencode_model_preferences_use_go_after_login(self):
        """With a stored Go credential the subscription model at the head of
        the list runs, and no paid opt-in is needed: the operator named it."""
        home = tempfile.mkdtemp()
        cache = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, home, True)
        self.addCleanup(shutil.rmtree, cache, True)
        auth = pathlib.Path(home) / ".local/share/opencode"
        auth.mkdir(parents=True)
        (auth / "auth.json").write_text(
            json.dumps({"opencode-go": {"type": "api", "key": "oc_sk_test"}}),
            encoding="utf-8")
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "kimi-k3", "providerID": "opencode-go",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1048576, "output": 131072},
         "cost": [{"input": 3, "output": 15}],
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode-go/kimi-k3", args
print(json.dumps({"type":"text","sessionID":"ses_pf2","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={
                **home_env(home),
                "XDG_CACHE_HOME": cache,
                "OPENPANDA_AGENT_MODELS": json.dumps(["opencode-go/kimi-k3"]),
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok")

    def test_opencode_bare_slug_preference_tries_each_provider(self):
        """A bare slug can be served by both providers. When the first hit is
        a Go model without a login, the same slug under Zen must still be
        considered instead of the whole preference being dropped."""
        home = tempfile.mkdtemp()
        cache = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, home, True)
        self.addCleanup(shutil.rmtree, cache, True)
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "kimi-k3", "providerID": "opencode-go",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1048576, "output": 131072},
         "cost": [{"input": 3, "output": 15}],
         "status": "active", "enabled": True},
        {"id": "kimi-k3", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1048576, "output": 131072},
         "cost": [{"input": 3, "output": 15}],
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/kimi-k3", args
print(json.dumps({"type":"text","sessionID":"ses_pf4","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={
                **home_env(home),
                "XDG_CACHE_HOME": cache,
                "OPENPANDA_AGENT_MODELS": json.dumps(["kimi-k3"]),
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertTrue(
            any("needs an OpenCode Go credential" in line for line in progress),
            progress,
        )

    def test_opencode_model_preferences_fall_back_when_none_served(self):
        """A preference list that names nothing this install serves does not
        leave the run without a model: the miss is reported and the free-first
        pick takes over."""
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "deepseek-v4.1-flash-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 200000, "output": 32000},
         "cost": [{"input": 0, "output": 0}],
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/deepseek-v4.1-flash-free", args
print(json.dumps({"type":"text","sessionID":"ses_pf3","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={"OPENPANDA_AGENT_MODELS": json.dumps(["opencode/nope-1.0"])},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertTrue(
            any("preferred model opencode/nope-1.0 is not served" in line
                for line in progress),
            progress,
        )

    def test_opencode_preference_list_walks_on_after_a_rejected_model(self):
        """A rejected entry moves the run to the NEXT preference, not off the
        list. The retry used to re-probe the roster and take the heuristic
        pick, so naming three models meant "try one, then whatever is free" —
        the operator's second and third choices were never tried."""
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "glm-5.3", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1000000, "output": 131072},
         "cost": [{"input": 1.4, "output": 4.4}],
         "status": "active", "enabled": True},
        {"id": "kimi-k3", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1048576, "output": 131072},
         "cost": [{"input": 3, "output": 15}],
         "status": "active", "enabled": True},
        {"id": "deepseek-v4.1-flash-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 200000, "output": 32000},
         "cost": [{"input": 0, "output": 0}],
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
if sys.argv[1:2] == ["run"]:
    args = sys.argv[1:]
    model = args[args.index("--model") + 1] if "--model" in args else ""
    if model.startswith("opencode/glm-5.3"):
        print(json.dumps({"type": "error", "error": {
            "type": "provider.no-route", "message": "Model unavailable: " + model}}))
        sys.exit(0)
    print(json.dumps({"type":"text","sessionID":"ses_walk","part":{"id":"px","type":"text","text":"ok on " + model}}))
    sys.exit(0)
print(json.dumps({"type":"text","sessionID":"ses_walk","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={"OPENPANDA_AGENT_MODELS": json.dumps(
                ["opencode/glm-5.3", "opencode/kimi-k3",
                 "opencode/deepseek-v4.1-flash-free"])},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok on opencode/kimi-k3")
        self.assertTrue(
            any("retrying on opencode/kimi-k3" in line for line in progress),
            progress,
        )

    def test_opencode_billing_retry_honours_the_preference_list(self):
        """A billing rejection is answered by the operator's list before the
        free tier: what they named is what they are willing to pay for. The
        retry used to jump straight to the free pick, so a paid model listed
        after a subscription one was unreachable."""
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "glm-5.3", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1000000, "output": 131072},
         "cost": [{"input": 1.4, "output": 4.4}],
         "status": "active", "enabled": True},
        {"id": "deepseek-v4.1-flash", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 1000000, "output": 384000},
         "cost": [{"input": 0.3, "output": 1.2}],
         "status": "active", "enabled": True},
        {"id": "mimo-v2.6-flash-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text"]},
         "limit": {"context": 200000, "output": 32000},
         "cost": [{"input": 0, "output": 0}],
         "status": "active", "enabled": True},
    ]}))
    sys.exit(0)
if sys.argv[1:2] == ["run"]:
    args = sys.argv[1:]
    model = args[args.index("--model") + 1] if "--model" in args else ""
    if model.startswith("opencode/glm-5.3"):
        print(json.dumps({"type": "error", "error": {
            "type": "api_error",
            "message": "Upstream request failed: Insufficient account funds"}}))
        sys.exit(0)
    print(json.dumps({"type":"text","sessionID":"ses_bp","part":{"id":"px","type":"text","text":"ok on " + model}}))
    sys.exit(0)
print(json.dumps({"type":"text","sessionID":"ses_bp","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={"OPENPANDA_AGENT_MODELS": json.dumps(
                ["opencode/glm-5.3", "opencode/deepseek-v4.1-flash"]),
                "OPENPANDA_OPENCODE_ALLOW_PAID": "1"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok on opencode/deepseek-v4.1-flash")
        self.assertTrue(
            any("opencode/deepseek-v4.1-flash" in line for line in progress),
            progress,
        )

    def test_opencode_injected_anthropic_endpoint_merges_native_config(self):
        """An injected Anthropic-dialect model reaches opencode through the
        Anthropic SDK provider, and the fragment is merged over the user's
        native config instead of replacing it (MCP servers must survive)."""
        native = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False)
        json.dump({"mcp": {"demo": {"type": "local", "command": ["true"]}},
                   "permission": {"external_directory": "allow"}}, native)
        native.close()
        self.addCleanup(os.unlink, native.name)
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, os, sys
args = sys.argv[1:]
assert "--standalone" in args, args
assert args[args.index("--model") + 1] == "panda/deepseek-flash", args
cfg = json.load(open(os.environ["OPENCODE_CONFIG"], encoding="utf-8"))
prov = cfg["provider"]["panda"]
assert prov["npm"] == "@ai-sdk/anthropic", prov
assert prov["options"]["baseURL"] == "{env:OPENPANDA_MODEL_BASE_URL}", prov
assert prov["models"]["deepseek-flash"]["name"] == "deepseek-flash", prov
assert cfg["mcp"]["demo"]["type"] == "local", cfg
assert cfg["permission"]["external_directory"] == "allow", cfg
print(json.dumps({"type":"text","sessionID":"ses_i1","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "OPENPANDA_MODEL_BASE_URL": "https://api.deepseek.com/anthropic",
                "OPENPANDA_MODEL_API_KEY": "sk-test",
                "OPENPANDA_MODEL_NAME": "deepseek-flash",
                "OPENPANDA_MODEL_API_TYPE": "anthropic",
                "OPENCODE_CONFIG": native.name,
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok")
        self.assertTrue(
            any("using injected model panda/deepseek-flash (anthropic)" in line
                for line in progress),
            progress,
        )

    def test_opencode_injected_openai_shorthand_gets_api_version(self):
        """The OpenAI SDK provider appends /chat/completions to the base, so
        OpenCode's harness shorthand must gain /v1; the Anthropic provider
        adds its own /v1 and keeps the shorthand."""
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, os, sys
cfg = json.load(open(os.environ["OPENCODE_CONFIG"], encoding="utf-8"))
assert cfg["provider"]["panda"]["npm"] == "@ai-sdk/openai-compatible", cfg
assert os.environ["OPENPANDA_MODEL_BASE_URL"] == "https://opencode.ai/zen/go/v1", os.environ
print(json.dumps({"type":"text","sessionID":"ses_i3","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "OPENPANDA_MODEL_BASE_URL": "https://opencode.ai/zen/go",
                "OPENPANDA_MODEL_API_KEY": "oc_sk_test",
                "OPENPANDA_MODEL_NAME": "deepseek-v4.1-flash",
                "OPENPANDA_MODEL_API_TYPE": "openai",
            },
        )
        self.assertTrue(payload["ok"], payload)

        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, os, sys
cfg = json.load(open(os.environ["OPENCODE_CONFIG"], encoding="utf-8"))
assert cfg["provider"]["panda"]["npm"] == "@ai-sdk/anthropic", cfg
assert os.environ["OPENPANDA_MODEL_BASE_URL"] == "https://opencode.ai/zen/go", os.environ
print(json.dumps({"type":"text","sessionID":"ses_i4","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "OPENPANDA_MODEL_BASE_URL": "https://opencode.ai/zen/go",
                "OPENPANDA_MODEL_API_KEY": "oc_sk_test",
                "OPENPANDA_MODEL_NAME": "deepseek-v4.1-flash",
                "OPENPANDA_MODEL_API_TYPE": "anthropic",
            },
        )
        self.assertTrue(payload["ok"], payload)

    def test_opencode_injected_openai_endpoint_keeps_compatible_provider(self):
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, os, sys
cfg = json.load(open(os.environ["OPENCODE_CONFIG"], encoding="utf-8"))
prov = cfg["provider"]["panda"]
assert prov["npm"] == "@ai-sdk/openai-compatible", prov
print(json.dumps({"type":"text","sessionID":"ses_i2","part":{"id":"px","type":"text","text":"ok"}}))
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "OPENPANDA_MODEL_BASE_URL": "https://api.deepseek.com/v1",
                "OPENPANDA_MODEL_API_KEY": "sk-test",
                "OPENPANDA_MODEL_NAME": "deepseek-flash",
                "OPENPANDA_MODEL_API_TYPE": "openai",
            },
        )
        self.assertTrue(payload["ok"], payload)

    def test_opencode_auto_pick_effort_maps_to_variant(self):
        """A requested reasoning effort rides the model's declared variant
        (provider/model#variant); an undeclared level leaves the bare id."""
        payload, _, _ = run_adapter(
            "opencode.py", "opencode",
            OC_HELP_CURRENT + self.OC_PICK_ROSTER + self.OC_API_MODELS + r'''
import json, sys
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/nemotron-3.5-super-vl-free#high", args
print(json.dumps({"type":"text","sessionID":"ses_p3","part":{"id":"px","type":"text","text":"ok"}}))
''',
            extra_request={"effort": "high"},
        )
        self.assertTrue(payload["ok"], payload)
        payload, _, _ = run_adapter(
            "opencode.py", "opencode",
            OC_HELP_CURRENT + self.OC_PICK_ROSTER + self.OC_API_MODELS + r'''
import json, sys
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/nemotron-3.5-super-vl-free", args
print(json.dumps({"type":"text","sessionID":"ses_p4","part":{"id":"px","type":"text","text":"ok"}}))
''',
            extra_request={"effort": "ultra"},
        )
        self.assertTrue(payload["ok"], payload)

    # A paid model that outscores every free entry on raw capability — the
    # default pick, which a simple task must still skip for a free model.
    OC_PAID_ROSTER = r'''
import sys
if sys.argv[1:2] == ["models"]:
    print("opencode/nemotron-3.5-super-vl-free")
    print("anthropic/claude-4.6-opus")
    sys.exit(0)
'''

    OC_PAID_API = r'''
import json, sys
if sys.argv[1:3] == ["api", "model.list"]:
    print(json.dumps({"data": [
        {"id": "nemotron-3.5-super-vl-free", "providerID": "opencode",
         "capabilities": {"tools": True, "input": ["text", "image"]},
         "limit": {"context": 1048576, "output": 131072},
         "status": "active", "enabled": True,
         "cost": [{"input": 0, "output": 0}],
         "variants": [{"id": "low"}, {"id": "medium"}, {"id": "high"}]},
        {"id": "claude-4.6-opus", "providerID": "anthropic",
         "capabilities": {"tools": True, "input": ["text", "image"]},
         "limit": {"context": 1000000, "output": 500000},
         "status": "active", "enabled": True,
         "cost": [{"input": 15, "output": 75}],
         "variants": [{"id": "low"}, {"id": "high"}]},
    ]}))
    sys.exit(0)
'''

    def test_opencode_simple_task_prefers_free_models(self):
        """Free is the default tier. A paid entry is selected automatically
        only when the operator opted in (OPENPANDA_OPENCODE_ALLOW_PAID=1);
        even then a simple task (complexity < 0.4) or a light effort request
        demotes every paid entry to last resort."""
        # Opted in and complex: capability decides, so the paid opus wins.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode",
            OC_HELP_CURRENT + self.OC_PAID_ROSTER + self.OC_PAID_API + r'''
import json, sys
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "anthropic/claude-4.6-opus", args
print(json.dumps({"type":"text","sessionID":"ses_f1","part":{"id":"px","type":"text","text":"ok"}}))
''',
            extra_request={"complexity": 0.9},
            env={"OPENPANDA_OPENCODE_ALLOW_PAID": "1"},
        )
        self.assertTrue(payload["ok"], payload)
        # No opt-in: the same complex task still takes the free model.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode",
            OC_HELP_CURRENT + self.OC_PAID_ROSTER + self.OC_PAID_API + r'''
import json, sys
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/nemotron-3.5-super-vl-free", args
print(json.dumps({"type":"text","sessionID":"ses_f0","part":{"id":"px","type":"text","text":"ok"}}))
''',
            extra_request={"complexity": 0.9},
        )
        self.assertTrue(payload["ok"], payload)
        # Simple task: even opted in, the free entry serves, and the progress
        # note says why.
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode",
            OC_HELP_CURRENT + self.OC_PAID_ROSTER + self.OC_PAID_API + r'''
import json, sys
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/nemotron-3.5-super-vl-free", args
print(json.dumps({"type":"text","sessionID":"ses_f2","part":{"id":"px","type":"text","text":"ok"}}))
''',
            extra_request={"complexity": 0.1},
            env={"OPENPANDA_OPENCODE_ALLOW_PAID": "1"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertTrue(
            any("free tier first" in line for line in progress),
            progress,
        )
        # A light effort request means the same thing — and rides the picked
        # model's matching variant.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode",
            OC_HELP_CURRENT + self.OC_PAID_ROSTER + self.OC_PAID_API + r'''
import json, sys
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/nemotron-3.5-super-vl-free#low", args
print(json.dumps({"type":"text","sessionID":"ses_f3","part":{"id":"px","type":"text","text":"ok"}}))
''',
            extra_request={"effort": "low"},
            env={"OPENPANDA_OPENCODE_ALLOW_PAID": "1"},
        )
        self.assertTrue(payload["ok"], payload)

    def test_opencode_simple_task_free_preference_without_catalog(self):
        """Without catalog coverage the provider id is the cost tell: only
        opencode's own provider counts as the free tier."""
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import sys
if sys.argv[1:2] == ["models"]:
    print("opencode/ll-alpha-free")
    print("anthropic/claude-4.6-opus")
    sys.exit(0)
if sys.argv[1:3] == ["api", "model.list"]:
    sys.exit(1)
''' + r'''
import json, sys
args = sys.argv[1:]
assert args[args.index("--model") + 1] == "opencode/ll-alpha-free", args
print(json.dumps({"type":"text","sessionID":"ses_f4","part":{"id":"px","type":"text","text":"ok"}}))
''',
            extra_request={"complexity": 0.2},
        )
        self.assertTrue(payload["ok"], payload)

    def test_opencode_reprobes_roster_and_retries_on_rejected_model(self):
        """Every dispatch identifies the served models first — and when the
        roster rotates mid-flight (the picked model retired between probe and
        run), the adapter drops its caches, re-identifies and retries on the
        fresh pick instead of reporting the task as failed."""
        # Probe 1 serves two models (deepseek wins the pick) and the run
        # rejects it; probe 2 serves a rotated set whose new flagship proves
        # the caches were actually dropped — the retry runs on it.
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, pathlib, sys
# Cross-call state lives in a file next to this fake CLI — each invocation
# is a fresh process, so env vars cannot carry the probe count.
probes = pathlib.Path(__file__).with_name("probes")
n = int(probes.read_text()) if probes.exists() else 0
probes.write_text(str(n + 1))
if sys.argv[1:2] == ["models"]:
    if n == 0:
        print("opencode/deepseek-v4.1-flash-free")
        print("opencode/mimo-v2.6-flash-free")
    else:
        print("opencode/mimo-v2.6-flash-free")
        print("opencode/nemotron-3.5-super-vl-free")
    sys.exit(0)
if sys.argv[1:3] == ["api", "model.list"]:
    sys.exit(1)
if sys.argv[1:2] == ["run"]:
    args = sys.argv[1:]
    model = args[args.index("--model") + 1] if "--model" in args else ""
    if model == "opencode/deepseek-v4.1-flash-free":
        # v2 answers a stale id with a provider.no-route error event.
        print(json.dumps({"type": "error", "error": {
            "type": "provider.no-route",
            "message": "Model unavailable: " + model}}))
        sys.exit(0)
    print(json.dumps({"type": "text", "sessionID": "ses_r1",
                      "part": {"id": "px", "type": "text",
                               "text": "ok on " + model}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        # The retry landed on the flagship only the re-probe could see.
        self.assertEqual(payload["result"],
                         "ok on opencode/nemotron-3.5-super-vl-free")
        self.assertTrue(
            any("auto-selected model opencode/deepseek-v4.1-flash-free" in l
                for l in progress), progress)
        self.assertTrue(
            any("rejected; retrying on opencode/nemotron-3.5-super-vl-free" in l
                for l in progress), progress)
        self.assertTrue(
            any("served by this install" in l for l in progress), progress)

    def test_opencode_current_stream_shape_contract(self):
        """The shipped 1.18.x CLI puts the payload on the event, not under
        "properties".

        Every field the adapter needs — part, sessionID, tokens, cost — sits at
        the top level (captured verbatim from `opencode run --format json`). The
        nested fixture above pins the older shape, which is why the adapter's
        property-only lookup stayed green in CI while producing nothing against
        the real CLI: no text was extracted, and the result fell through to the
        stderr log wall.
        """
        payload, progress, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json
print(json.dumps({"type":"step_start","timestamp":1789547927287,"sessionID":"ses_real","part":{"id":"prt_s","messageID":"msg_1","sessionID":"ses_real","type":"step-start"}}))
print(json.dumps({"type":"tool_use","timestamp":1789548046933,"sessionID":"ses_real","part":{"id":"prt_t","messageID":"msg_1","sessionID":"ses_real","type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"uname -m"},"output":"arm64\n","title":"uname -m"}}}))
print(json.dumps({"type":"step_finish","timestamp":1789548046939,"sessionID":"ses_real","part":{"id":"prt_f1","messageID":"msg_1","sessionID":"ses_real","type":"step-finish","reason":"tool-calls","tokens":{"total":9667,"input":25,"output":42,"reasoning":0,"cache":{"write":0,"read":9600}},"cost":0}}))
print(json.dumps({"type":"text","timestamp":1789547927591,"sessionID":"ses_real","part":{"id":"prt_x","messageID":"msg_1","sessionID":"ses_real","type":"text","text":"`arm64`"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "`arm64`")
        self.assertEqual(payload["session_id"], "ses_real")
        self.assertEqual(payload["usage"], {"input_tokens": 25, "output_tokens": 42})
        self.assertEqual(payload["tokens"], 67)
        self.assertTrue(
            any("bash" in line for line in progress),
            "a completed tool call must surface as a progress note: %r" % (progress,),
        )

    def test_opencode_session_id_requires_ses_prefix(self):
        """The event-envelope `id` (a UUID) must never mint the session handle.

        A poisoned handle travels back as the task's resume checkpoint: every
        follow-up `--session <uuid>` hits opencode's "Expected a string
        starting with ses" 400, cold-starts, and re-burns the whole run —
        the exact loop this regression prevents. Only ses_-prefixed ids are
        real handles.
        """
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json
# Envelope-level `id` is the EVENT's own uuid, not a session.
print(json.dumps({"type":"session.updated","id":"bc47c3fd-29e9-4cbf-91b0-e4c84918f54b"}))
print(json.dumps({"type":"text","sessionID":"ses_real1","part":{"id":"px","type":"text","text":"ok"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["session_id"], "ses_real1")

        # UUIDs everywhere and no ses_ id: the handle stays empty — a cold
        # start next round beats resuming a handle that can only fail.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json
print(json.dumps({"type":"session.updated","id":"bc47c3fd-29e9-4cbf-91b0-e4c84918f54b"}))
print(json.dumps({"type":"text","part":{"id":"px","type":"text","text":"ok"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        # Empty handles are omitted from the envelope entirely.
        self.assertEqual(payload.get("session_id", ""), "")

    def test_opencode_timeout_reports_session_checkpoint(self):
        """A watchdog-killed run must still report the session it minted.

        The session is the checkpoint the orchestrator's retry resumes —
        dropping it makes every timeout a cold start, which is the loop that
        kept long tasks from ever finishing."""
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys, time
if sys.argv[1:2] == ["models"]:
    sys.exit(0)
if sys.argv[1:3] == ["api", "model.list"]:
    sys.exit(1)
if sys.argv[1:2] == ["run"]:
    print(json.dumps({"type":"text","sessionID":"ses_t1",
                      "part":{"id":"px","type":"text","text":"still working"}}))
    sys.stdout.flush()
    time.sleep(10)
''',
            extra_request={"timeout_s": 2},
            timeout=8,
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 124)
        self.assertEqual(payload["session_id"], "ses_t1")

    def test_opencode_announces_session_event_early(self):
        """The adapter announces the minted session as an event, not only in
        the result envelope: a run the harness kills mid-stream (stall,
        tool-call fuse, host hard limit) produces no envelope, so the event
        is the only channel a checkpoint can ride."""
        payload, lines, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:2] == ["models"]:
    sys.exit(0)
if sys.argv[1:3] == ["api", "model.list"]:
    sys.exit(1)
print(json.dumps({"type":"text","sessionID":"ses_ann",
                  "part":{"id":"px","type":"text","text":"ok"}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        events = [json.loads(l) for l in lines if l.startswith("{")]
        sessions = [e for e in events
                    if e.get("type") == "event" and e.get("ev") == "session"]
        self.assertTrue(sessions, f"no session event in {lines}")
        self.assertEqual(sessions[0]["id"], "ses_ann")
        self.assertEqual(payload["session_id"], "ses_ann")

    def test_claude_announces_session_event_early(self):
        payload, lines, _ = run_adapter(
            "claude_code.py", "claude", r'''
import json, sys
print(json.dumps({"type":"system","subtype":"init","session_id":"sess-ann"}))
print(json.dumps({"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]},"session_id":"sess-ann"}))
print(json.dumps({"type":"result","is_error":False,"result":"hi","session_id":"sess-ann"}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        events = [json.loads(l) for l in lines if l.startswith("{")]
        sessions = [e for e in events
                    if e.get("type") == "event" and e.get("ev") == "session"]
        self.assertTrue(sessions, f"no session event in {lines}")
        self.assertEqual(sessions[0]["id"], "sess-ann")

    def test_opencode_provider_failure_reports_session_checkpoint(self):
        """A mid-run provider error rides the result with the minted session —
        the orchestrator's retry can resume the conversation instead of
        cold-starting."""
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
if sys.argv[1:2] == ["models"]:
    sys.exit(0)
if sys.argv[1:3] == ["api", "model.list"]:
    sys.exit(1)
if sys.argv[1:2] == ["run"]:
    print(json.dumps({"type":"text","sessionID":"ses_p9",
                      "part":{"id":"px","type":"text","text":"work"}}))
    print(json.dumps({"type":"error","error":{"type":"api",
                      "message":"rate limit exceeded"}}))
''',
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["session_id"], "ses_p9")

    def test_opencode_textless_stream_fails_instead_of_reporting_success(self):
        """Events with no assistant text must be a failure, not a green result.

        Returning the stderr log wall as "ok" is what turned one broken run into
        a task that could never finish: the supervisor judge saw no answer, kept
        re-running the agent until the budget was spent, and parked the task in
        review. A failure hands the work to the next adapter in the chain.
        """
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
print(json.dumps({"type":"step_start","sessionID":"ses_x","part":{"id":"p","type":"step-start"}}))
sys.stderr.write("timestamp=2026-01-01T00:00:00Z level=INFO message=noise\n")
''',
        )
        self.assertFalse(payload["ok"], payload)
        self.assertIn("no assistant text", payload["result"])
        # The stderr tail is bounded diagnostics, never the whole log.
        self.assertLessEqual(len(payload["result"].splitlines()), 2, payload["result"])

    def test_opencode_resume_and_plain_fallback_contract(self):
        # Resume threads --session through to the CLI.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
args = sys.argv[1:]
assert "--session" in args and args[args.index("--session") + 1] == "ses-prev"
print(json.dumps({"type":"message.part.updated","properties":{"part":{"id":"p","type":"text","text":"ok"}}}))
''',
            extra_request={"resume": "ses-prev"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "ok")
        # A build whose help predates --format json (and named the permission
        # flag --dangerously-skip-permissions) goes straight to the plain text
        # run — no doomed stream attempt, and the legacy flag, not --auto.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_LEGACY + r'''
import sys
args = sys.argv[1:]
assert "--format" not in args
assert args[:2] == ["run", "--print-logs=false"]
assert "--dangerously-skip-permissions" in args
assert "--auto" not in args
# The plain path always carries a resolvable model: with no env model set it
# must be the built-in default, never the empty string.
assert "--model" in args
assert args[args.index("--model") + 1] == "opencode/deepseek-v4.1-flash-free"
print("plain opencode answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "plain opencode answer")

    def test_opencode_restricted_contract(self):
        # A restricted request (unconsented remote task) narrows the run to
        # opencode's built-in read-only agent: --agent plan lands on the
        # command line next to --model, while --auto stays (it only lifts
        # permissions that are not explicitly denied, and plan's denials are
        # explicit).
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_CURRENT + r'''
import json, sys
args = sys.argv[1:]
assert "--agent" in args and args[args.index("--agent") + 1] == "plan", args
assert "--auto" in args, args
print(json.dumps({"type":"message.part.updated","properties":{"part":{"id":"p","type":"text","text":"readonly run"}}}))
''',
            extra_request={"restricted": True},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "readonly run")
        # A build whose help has no --agent cannot express the narrow face at
        # all, so a restricted request is refused instead of running
        # full-power — degrading to the default agent is the exact hole the
        # mode exists to close.
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", OC_HELP_LEGACY + r'''
import sys
args = sys.argv[1:]
assert "--agent" not in args, args
print("plain opencode answer")
''',
            extra_request={"restricted": True},
        )
        self.assertFalse(payload["ok"], payload)
        self.assertIn("no --agent flag", payload["result"])

    def test_grok_session_contract(self):
        # Every run mints its own session and returns it as session_id. grok
        # types --session-id as a UUID, so the handle it is given must be one:
        # a name-shaped id fails the run before any work starts.
        payload, _, _ = run_adapter(
            "grok_build.py", "grok", r'''
import json, sys, uuid
args = sys.argv[1:]
sid = args[args.index("-s") + 1]
assert str(uuid.UUID(sid)) == sid, sid
assert "-r" not in args, args
assert "--single" in args and "contract prompt" in args
# streaming-json, not plain: this build prints its answer and then keeps
# running, so the adapter needs an event stream with a terminal line.
assert args[args.index("--output-format") + 1] == "streaming-json"
assert "--always-approve" in args
print(json.dumps({"type": "text", "data": "grok "}), flush=True)
print(json.dumps({"type": "text", "data": "answer"}), flush=True)
print(json.dumps({"type": "usage", "usage": {}}), flush=True)
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "grok answer")
        first = payload.get("session_id", "")
        self.assertEqual(str(uuid.UUID(first)), first, payload)
        # A stored handle is NOT resumed: grok 1.0.67 cannot resume headless
        # (-r with --single answers nothing at all, and -s with an existing id
        # is refused as "already in use"), so a round cold-starts with a fresh
        # UUID and reports that one.
        prev = "0f5c1d3e-2b44-4a71-9c8e-6d1b2a3f4c55"
        payload, _, _ = run_adapter(
            "grok_build.py", "grok", r'''
import json, sys, uuid
args = sys.argv[1:]
assert "-r" not in args, args
sid = args[args.index("-s") + 1]
assert str(uuid.UUID(sid)) == sid, sid
assert sid != "0f5c1d3e-2b44-4a71-9c8e-6d1b2a3f4c55", "the stored handle must not be reused as a new session id"
print(json.dumps({"type": "text", "data": "grok fresh"}), flush=True)
print(json.dumps({"type": "usage", "usage": {}}), flush=True)
''',
            extra_request={"resume": prev},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "grok fresh")
        self.assertNotEqual(payload.get("session_id"), prev)
        # A build too old for streaming-json falls back to the one-shot plain
        # run, which those builds do exit from — nameless, since -s may be
        # missing there too.
        payload, _, _ = run_adapter(
            "grok_build.py", "grok", r'''
import sys
args = sys.argv[1:]
if args[args.index("--output-format") + 1] != "plain":
    sys.stderr.write("error: invalid value 'streaming-json'\n")
    sys.exit(1)
assert "-s" not in args and "-r" not in args
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

    def test_deepseek_harness_model_patch_caps_provider_tokens(self):
        payload, _, _ = run_adapter(
            "deepseek_harness.py", "dsh", r'''
import pathlib, sys
args = sys.argv[1:]
assert args[:2] == ["--profile", "headless"]
patch = pathlib.Path(args[args.index("--patch") + 1]).read_text()
assert "id: agent-default-model" in patch
assert 'model: "glm-5.3-flash"' in patch
assert "id: llm-deepseek" in patch
assert "maxTokens: 131072" in patch
assert args[-1] == "contract prompt"
print("dsh injected answer")
''',
            env={"DEEPSEEK_MODEL": "glm-5.3-flash"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "dsh injected answer")
        payload, _, _ = run_adapter(
            "hermes.py", "hermes", r'''
import sys
assert sys.argv[1:] == ["--cli", "--yolo", "-z", "contract prompt"]
print("hermes answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "hermes answer")
        # Hook auto-approval is opt-in only: the flag rides under the env
        # switch, never in the default face.
        payload, _, _ = run_adapter(
            "hermes.py", "hermes", r'''
import sys
assert sys.argv[1:] == ["--cli", "--yolo", "--accept-hooks", "-z", "contract prompt"]
print("hermes hooks answer")
''',
            env={"OPENPANDA_ACCEPT_HOOKS": "1"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "hermes hooks answer")
        payload, _, _ = run_adapter(
            "openclaw.py", "openclaw", r'''
import sys
assert sys.argv[1:] == ["agent", "exec", "contract prompt"]
print("openclaw answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "openclaw answer")

    def test_generic_command_template_contract(self):
        # The card-declared template is shlex-split; {prompt} substitutes as
        # ONE literal argv element, so prompt whitespace/quotes cannot split
        # or reach a shell.
        payload, _, _ = run_adapter(
            "generic.py", "zcode", r'''
import sys
assert sys.argv[1:] == ["--prompt", "contract prompt"], sys.argv
print("zcode answer")
''',
            extra_request={"cmd": "zcode --prompt {prompt}"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "zcode answer")

    def test_generic_prompt_inside_flag_and_default_append(self):
        # Embedded form: "--prompt={prompt}" keeps the prompt inside one argv
        # element.
        payload, _, _ = run_adapter(
            "generic.py", "mimo", r'''
import sys
assert sys.argv[1:] == ["--prompt=contract prompt", "--quiet"], sys.argv
print("mimo answer")
''',
            extra_request={"cmd": "mimo --prompt={prompt} --quiet"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "mimo answer")
        # No placeholder at all: the prompt is appended as the last argument.
        payload, _, _ = run_adapter(
            "generic.py", "mimo", r'''
import sys
assert sys.argv[1:] == ["exec", "contract prompt"], sys.argv
print("appended")
''',
            extra_request={"cmd": "mimo exec"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "appended")

    def test_generic_empty_template_is_reported(self):
        # No command declared (or an unparseable one) is a configuration
        # error, exit 2 — the adapter never guesses an argv.
        payload, _, _ = run_adapter(
            "generic.py", "never-spawned", "import sys; sys.exit(99)\n",
            extra_request={"cmd": ""},
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 2)
        self.assertIn("command", payload["result"])

    def test_generic_timeout_and_missing_binary_contract(self):
        payload, _, _ = run_adapter(
            "generic.py", "slowcli", "import time; time.sleep(10)\n",
            # The watchdog is the thing under test: pin a small timeout_s so
            # the adapter kills the fake CLI well inside the 8s outer bound.
            extra_request={"cmd": "slowcli {prompt}", "timeout_s": 2},
            timeout=8,
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 124)
        payload, _, _ = run_adapter(
            "generic.py", "ghostcli", "import sys\n",
            extra_request={"cmd": "definitely-not-on-path-xyz {prompt}"},
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 127)

    @unittest.skipIf(
        sys.platform == "win32",
        "Windows has no exec bit: an unrunnable file is indistinguishable from "
        "a missing one there (CreateProcess refuses it with the same error), "
        "so the 126 convention only exists on POSIX.",
    )
    def test_generic_not_executable_is_126(self):
        # A binary on PATH that lacks the exec bit is a spawn failure, not a
        # missing binary: 126 follows the shell convention.
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            (tmp / "noexec").write_text("#!/usr/bin/env python3\n")  # no +x
            merged = os.environ.copy()
            merged["PATH"] = str(tmp) + os.pathsep + merged.get("PATH", "")
            req = {"prompt": "p", "timeout_s": 30, "cmd": "noexec {prompt}"}
            proc = subprocess.run(
                [sys.executable, str(ADAPTERS / "generic.py")],
                input=json.dumps(req), text=True, capture_output=True,
                env=merged, cwd=str(ROOT), timeout=10,
            )
            payload = json.loads(proc.stdout.strip())
            self.assertFalse(payload["ok"], payload)
            self.assertEqual(payload["exit_code"], 126)

    def test_generic_stdin_placeholder_pipes_prompt(self):
        # {stdin} drops its element and delivers the prompt on the child's
        # stdin — the headless contract for CLIs that read it there, and the
        # way past the OS argv limit for large prompts.
        payload, _, _ = run_adapter(
            "generic.py", "mimo", r'''
import sys
assert sys.argv[1:] == ["exec", "-"], sys.argv
print("stdin:" + sys.stdin.read())
''',
            extra_request={"cmd": "mimo exec - {stdin}"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "stdin:contract prompt")

    def test_generic_optional_placeholders(self):
        # {cwd} always resolves; {resume}/{max_turns} fill in when the
        # request carries them.
        payload, _, _ = run_adapter(
            "generic.py", "mimo", r'''
import os, sys
args = sys.argv[1:]
assert os.path.basename(os.path.normpath(args[args.index("--dir") + 1])) == "work", args
assert args[args.index("--session") + 1] == "sess-1", args
assert args[args.index("--max-agent-turns") + 1] == "7", args
assert "contract prompt" in args, args
print("filled")
''',
            extra_request={
                "cmd": "mimo --dir {cwd} --session {resume} "
                       "--max-agent-turns {max_turns} {prompt}",
                "resume": "sess-1", "max_turns": 7,
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "filled")
        # Unset optional placeholders drop cleanly: the joined form loses its
        # whole element, the two-token form also loses the flag that would
        # otherwise eat the next element as its value.
        payload, _, _ = run_adapter(
            "generic.py", "mimo", r'''
import sys
assert sys.argv[1:] == ["run", "contract prompt"], sys.argv
print("dropped")
''',
            extra_request={
                "cmd": "mimo run --session {resume} --turns={max_turns} {prompt}",
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "dropped")

    def test_generic_prompt_text_is_never_rescanned(self):
        # Single-pass substitution: literal "{cwd}" inside the prompt body
        # survives verbatim — expansion must not re-scan substituted values.
        payload, _, _ = run_adapter(
            "generic.py", "mimo", r'''
import sys
args = sys.argv[1:]
assert args[args.index("--p") + 1] == "fix {cwd} and {resume}", args
print("literal")
''',
            extra_request={
                "cmd": "mimo --p {prompt}",
                "prompt": "fix {cwd} and {resume}",
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "literal")

    def test_generic_template_expanding_to_nothing_is_reported(self):
        # A template whose only element was an unset optional placeholder
        # expands to zero argv: report a config error instead of exec'ing
        # the prompt itself as the command.
        payload, _, _ = run_adapter(
            "generic.py", "never-spawned", "import sys; sys.exit(99)\n",
            extra_request={"cmd": "{resume}"},
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 2)

    def test_pi_json_mode_contract(self):
        # `pi --mode json` emits a session header plus JSONL events; the
        # adapter reduces it to the wire result: last assistant text is the
        # answer, the session id rides for resume, usage sums across
        # messages, and tool executions become progress/event rows.
        payload, progress, _ = run_adapter(
            "pi.py", "pi", r'''
import json, os, sys
args = sys.argv[1:]
assert args[:2] == ["--mode", "json"], args
assert "--approve" in args
assert "--tools" in args and "read" in args[args.index("--tools") + 1]
assert "--exclude-tools" in args and "mcp__*" in args
assert args[-2:] == ["--", "contract prompt"], args
assert os.path.basename(os.getcwd()) == "work"
print(json.dumps({"type":"session","id":"pi-sess-1","version":1}))
print(json.dumps({"type":"tool_execution_start","toolCallId":"t1","toolName":"bash","args":{"command":"pwd"}}))
print(json.dumps({"type":"tool_execution_end","toolCallId":"t1","isError":False,"result":"/work"}))
print(json.dumps({"type":"message_end","message":{"role":"assistant","id":"m1",
      "content":[{"type":"text","text":"pi answer"}],
      "usage":{"input":4,"output":6,"cacheRead":2,"cacheWrite":1,"cost":{"total":0.03}}}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "pi answer")
        self.assertEqual(payload["session_id"], "pi-sess-1")
        self.assertEqual(payload["tokens"], 10)
        self.assertEqual(payload["cost"], 0.03)
        self.assertEqual(payload["usage"]["cache_read_tokens"], 2)
        self.assertTrue(any("bash: pwd" in line for line in progress), progress)

    def test_pi_resume_policy_and_restricted_contract(self):
        # A follow-up round resumes the session by id; extended policy drops
        # the --tools whitelist entirely.
        payload, _, _ = run_adapter(
            "pi.py", "pi", r'''
import json, sys
args = sys.argv[1:]
assert "--session" in args and args[args.index("--session") + 1] == "pi-prev"
assert "--tools" not in args, args
print(json.dumps({"type":"message_end","message":{"role":"assistant",
      "content":[{"type":"text","text":"resumed"}],"usage":{"input":1,"output":1}}}))
''',
            extra_request={"resume": "pi-prev", "tools_policy": "extended"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "resumed")
        # Restricted (unconsented remote) narrows to read-only tools and
        # switches project resources off — no approve, no MCP, no extensions.
        payload, _, _ = run_adapter(
            "pi.py", "pi", r'''
import json, sys
args = sys.argv[1:]
assert "--tools" in args
assert args[args.index("--tools") + 1] == "read,grep,find,ls", args
assert "--no-mcp" in args and "--no-extensions" in args
assert "--no-approve" in args and "--approve" not in args
print(json.dumps({"type":"message_end","message":{"role":"assistant",
      "content":[{"type":"text","text":"readonly"}],"usage":{"input":1,"output":1}}}))
''',
            extra_request={"restricted": True},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "readonly")

    def test_pi_print_fallback_contract(self):
        # A pi too old for --mode json degrades to print mode — the command
        # is rebuilt wholesale, never token-filtered.
        payload, _, _ = run_adapter(
            "pi.py", "pi", r'''
import sys
args = sys.argv[1:]
if "--mode" in args:
    sys.stderr.write("error: unknown option --mode\n")
    sys.exit(1)
assert args[0] == "--print" and args[-1] == "contract prompt", args
print("pi print answer")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "pi print answer")

    def test_pi_injected_model_contract(self):
        # Injection writes a temp agent dir whose models.json declares the
        # "panda" provider. The api key field carries the bare ENV VAR NAME:
        # pi resolves a non-secret string as "read this variable" (docs/
        # models.md "Value Resolution"), so the secret never lands in the file
        # or argv. A "${PI_API_KEY}" template is NOT interpolated by pi — it is
        # sent as the literal string and the provider answers 401.
        payload, _, _ = run_adapter(
            "pi.py", "pi", r'''
import json, os, sys
args = sys.argv[1:]
assert "--model" in args and args[args.index("--model") + 1] == "panda/injected-model"
agent_dir = os.environ.get("PI_CODING_AGENT_DIR", "")
assert agent_dir, "PI_CODING_AGENT_DIR must point at the temp dir"
with open(os.path.join(agent_dir, "models.json")) as f:
    doc = json.load(f)
prov = doc["providers"]["panda"]
assert prov.get("apiKey") == "PI_API_KEY", doc
assert "${" not in json.dumps(prov), doc
assert "sekret-key" not in json.dumps(prov)
assert "--session-dir" in args, args
print(json.dumps({"type":"message_end","message":{"role":"assistant",
      "content":[{"type":"text","text":"injected"}],"usage":{"input":1,"output":1}}}))
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "PI_MODEL": "injected-model",
                "PI_BASE_URL": "https://api.example/v1",
                "PI_API_KEY": "sekret-key",
                "OPENPANDA_MODEL_API_TYPE": "openai",
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "injected")

    def test_generic_extended_placeholders(self):
        # task_id / effort / system_prompt / timeout_s fill in when the
        # request carries them, and {env:NAME} resolves from the sandboxed
        # environment — a missing variable drops flag and element together.
        payload, _, _ = run_adapter(
            "generic.py", "mimo", r'''
import os, sys
args = sys.argv[1:]
assert args[args.index("--task") + 1] == "task-42", args
assert args[args.index("--effort") + 1] == "high", args
assert args[args.index("--sysp") + 1] == "static protocol text", args
assert args[args.index("--deadline") + 1] == "30", args
assert args[args.index("--key") + 1] == "sekrit", args
assert "--other" not in args, args
assert "contract prompt" in args, args
print("filled")
''',
            env={"MIMO_API_KEY": "sekrit"},
            extra_request={
                "cmd": "mimo --task {task_id} --effort {effort} "
                       "--sysp {system_prompt} --deadline {timeout_s} "
                       "--key {env:MIMO_API_KEY} --other {env:MIMO_MISSING} {prompt}",
                "task_id": "task-42", "effort": "high",
                "system_prompt": "static protocol text",
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "filled")

    def test_antigravity_envelope_contract(self):
        # agy -p … --output-format json emits ONE JSON envelope; the adapter
        # reduces it to the wire result and surfaces conversation_id for
        # resume.
        payload, _, _ = run_adapter(
            "antigravity.py", "agy", r'''
import json, sys
args = sys.argv[1:]
assert args[:2] == ["-p", "contract prompt"], args
assert "--output-format" in args and "json" in args
assert "--dangerously-skip-permissions" in args
assert "--conversation" not in args
print(json.dumps({"conversation_id":"conv-1","status":"SUCCESS",
                  "response":"agy answer",
                  "usage":{"input_tokens":9,"output_tokens":4,"thinking_tokens":2,"total_tokens":15}}))
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "agy answer")
        self.assertEqual(payload["session_id"], "conv-1")
        self.assertEqual(payload["tokens"], 15)
        self.assertEqual(payload["usage"]["output_tokens"], 6)

    def test_antigravity_resume_and_failure_contract(self):
        # A follow-up round resumes the previous conversation by id.
        payload, _, _ = run_adapter(
            "antigravity.py", "agy", r'''
import json, sys
args = sys.argv[1:]
assert "--conversation" in args and args[args.index("--conversation") + 1] == "conv-prev"
print(json.dumps({"conversation_id":"conv-prev","status":"SUCCESS","response":"resumed"}))
''',
            extra_request={"resume": "conv-prev"},
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "resumed")
        # A non-SUCCESS envelope (or a bare error line) is a failure, and a
        # stray event object must not satisfy the envelope lookup.
        payload, _, _ = run_adapter(
            "antigravity.py", "agy", r'''
import json, sys
print(json.dumps({"type":"event","detail":"noise"}))
print(json.dumps({"status":"FAILED","conversation_id":"conv-x"}))
sys.stderr.write("authentication required\n")
sys.exit(1)
''',
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 1)
        # The FAILED envelope carried no response text, so the stderr
        # diagnosis is what the operator sees.
        self.assertIn("authentication required", payload["result"])

    def test_antigravity_injected_run_selects_gemini_provider(self):
        # An injected run must land agy on its Gemini-API-key branch: without
        # {"modelProvider":"gemini"} the CLI ignores GEMINI_API_KEY and
        # GOOGLE_GEMINI_BASE_URL entirely and opens a Google OAuth flow, which
        # headless reads as "authentication required" — i.e. injection looks
        # like it did nothing.
        #
        # HOME/USERPROFILE point at a throwaway dir so the adapter's
        # settings.json write cannot touch the developer's real ~/.gemini.
        home = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, home, True)
        settings = pathlib.Path(home) / ".gemini" / "antigravity-cli" / "settings.json"
        settings.parent.mkdir(parents=True)
        settings.write_text(json.dumps({"theme": "dark"}))
        payload, _, _ = run_adapter(
            "antigravity.py", "agy", r'''
import json, os, sys
p = os.path.join(os.environ.get("HOME") or os.environ.get("USERPROFILE"),
                 ".gemini", "antigravity-cli", "settings.json")
doc = json.load(open(p, encoding="utf-8"))
print(json.dumps({"conversation_id": "c1", "status": "SUCCESS",
                  "response": "provider=%s theme=%s" % (doc.get("modelProvider"), doc.get("theme"))}))
''',
            env={"OPENPANDA_INJECTED_MODEL": "1", "HOME": home, "USERPROFILE": home},
        )
        # The CLI sees the switch while it runs — and the operator's own
        # preferences are merged in, not overwritten.
        self.assertIn("provider=gemini", payload.get("result", ""))
        self.assertIn("theme=dark", payload.get("result", ""))
        # … and the file is put back when the run ends. Leaving it rewritten
        # would pin the operator's interactive agy to the API-key branch, where
        # it answers "authentication required" instead of using its OAuth
        # session, with no way back except hand-editing.
        self.assertEqual(json.loads(settings.read_text()), {"theme": "dark"})

        # A run that is NOT injected leaves the file alone — an account login
        # the operator configured by hand must not be switched underneath them.
        other = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, other, True)
        keep = pathlib.Path(other) / ".gemini" / "antigravity-cli" / "settings.json"
        keep.parent.mkdir(parents=True)
        keep.write_text(json.dumps({"modelProvider": "oauth-personal"}))
        run_adapter(
            "antigravity.py", "agy", r'''
import json, sys
print(json.dumps({"conversation_id":"c2","status":"SUCCESS","response":"own auth"}))
''',
            env={"HOME": other, "USERPROFILE": other},
        )
        self.assertEqual(json.loads(keep.read_text()).get("modelProvider"), "oauth-personal")

    def test_hermes_injected_run_writes_provider_config(self):
        # hermes 0.19 ignores OPENAI_BASE_URL/OPENAI_API_KEY and resolves its
        # model from $HERMES_HOME/config.yaml, so injection only works if the
        # adapter materialises a config. The endpoint and key must ride the
        # environment as ${ENV} references: a literal key in the file would be
        # a credential on disk for the life of the run.
        payload, _, _ = run_adapter(
            "hermes.py", "hermes", r'''
import os, sys
args = sys.argv[1:]
assert args[:3] == ["--cli", "--yolo", "-z"], args
home = os.environ.get("HERMES_HOME", "")
assert home, "HERMES_HOME must point at the generated config"
with open(os.path.join(home, "config.yaml"), encoding="utf-8") as f:
    cfg = f.read()
assert "provider: panda" in cfg, cfg
assert "model: injected-model" in cfg, cfg
assert "${OPENPANDA_INJECT_BASE_URL}" in cfg, cfg
assert "key_env: OPENPANDA_INJECT_API_KEY" in cfg, cfg
assert "sekret-key" not in cfg, "the key must never be written to the config"
assert os.environ["OPENPANDA_INJECT_BASE_URL"] == "https://api.example/v1"
assert os.environ["OPENPANDA_INJECT_API_KEY"] == "sekret-key"
print("hermes answer")
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "OPENAI_BASE_URL": "https://api.example/v1",
                "OPENAI_API_KEY": "sekret-key",
                "OPENAI_MODEL": "injected-model",
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "hermes answer")

    def test_hermes_plain_run_leaves_hermes_home_alone(self):
        # No injection → no HERMES_HOME override: the CLI keeps reading the
        # operator's own home (skills, sessions, hooks) exactly as before.
        payload, _, _ = run_adapter(
            "hermes.py", "hermes", r'''
import os, sys
assert sys.argv[1:3] == ["--cli", "--yolo"]
assert "HERMES_HOME" not in os.environ, os.environ.get("HERMES_HOME")
print("own model")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "own model")

    def test_openclaw_injected_run_pins_config_state_and_home(self):
        # An injected openclaw turn must not touch three things: the ambient
        # config (--config), the gateway state (--state-dir) and the
        # auth-profile store under ~/.openclaw — the last one has no flag, so
        # HOME/USERPROFILE are redirected instead. The key rides a secret
        # reference (source: env), never the config file.
        payload, _, _ = run_adapter(
            "openclaw.py", "openclaw", r'''
import json, os, sys
args = sys.argv[1:]
assert args[:2] == ["agent", "exec"], args
cfg = args[args.index("--config") + 1]
state = args[args.index("--state-dir") + 1]
assert args[args.index("--model") + 1] == "panda/injected-model", args
assert "--json" in args, args
with open(cfg, encoding="utf-8") as f:
    doc = json.load(f)
prov = doc["models"]["providers"]["panda"]
assert prov["baseUrl"] == "https://api.example/v1", prov
assert prov["api"] == "openai-completions", prov
assert prov["apiKey"] == {"source": "env", "provider": "pandaenv",
                          "id": "OPENCLAW_API_KEY"}, prov
assert "sekret-key" not in json.dumps(doc), "the key must not reach the config"
assert doc["secrets"]["providers"]["pandaenv"] == {"source": "env"}, doc
assert os.path.isdir(state), state
home = os.environ["HOME"]
assert os.path.normcase(home) in (os.path.normcase(os.environ["USERPROFILE"]),), os.environ["USERPROFILE"]
assert os.path.isdir(home), home
print(json.dumps({"ok": True, "status": "ok", "final": "openclaw answer",
                  "payloads": [{"text": "openclaw answer"}],
                  "model": "injected-model", "provider": "panda",
                  "sessionId": "sess-oc",
                  "usage": {"input": 7, "output": 3, "cacheRead": 2,
                            "cacheWrite": 0, "total": 10}}))
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "OPENAI_BASE_URL": "https://api.example/v1",
                "OPENCLAW_API_KEY": "sekret-key",
                "OPENCLAW_MODEL": "injected-model",
                "OPENPANDA_MODEL_API_TYPE": "openai",
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "openclaw answer")
        self.assertEqual(payload["session_id"], "sess-oc")
        self.assertEqual(payload["tokens"], 10)
        self.assertEqual(payload["usage"], {
            "input_tokens": 7, "output_tokens": 3,
            "cache_read_tokens": 2, "cache_write_tokens": 0,
        })

    def test_openclaw_envelope_failure_is_not_a_pass(self):
        # ok:false in the envelope is a failed turn even when the process
        # exits 0 — the CLI reports provider/auth errors that way.
        payload, _, _ = run_adapter(
            "openclaw.py", "openclaw", r'''
import json, sys
print(json.dumps({"ok": False, "status": "error", "final": "",
                  "payloads": [],
                  "error": {"message": "auth profile needs migration"}}))
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "OPENAI_BASE_URL": "https://api.example/v1",
                "OPENCLAW_API_KEY": "k",
                "OPENCLAW_MODEL": "injected-model",
            },
        )
        self.assertFalse(payload["ok"], payload)
        self.assertIn("auth profile needs migration", payload["result"])

    def test_zcode_injected_run_writes_model_selection(self):
        # zcode refuses to send anything until a model is selected, and the
        # selection lives in the personal provider config — not in ZCODE_*.
        # defaultModelSelection must sit INSIDE config: a top-level copy is
        # dropped silently and the CLI keeps asking for a model.
        payload, _, _ = run_adapter(
            "zcode.py", "zcode", r'''
import json, os, sys
args = sys.argv[1:]
assert args[args.index("--prompt") + 1] == "contract prompt", args
assert "--json" in args, args
path = os.environ.get("ZCODE_PERSONAL_PROVIDER_CONFIG_FILE", "")
assert path and os.path.isfile(path), path
with open(path, encoding="utf-8") as f:
    doc = json.load(f)
cfg = doc["config"]
assert cfg["defaultModelSelection"] == {"providerId": "panda",
                                        "modelId": "injected-model"}, cfg
assert "defaultModelSelection" not in doc, "must live inside config"
rule = cfg["providerConfigRules"]["providerRules"][0]
assert rule["providerId"] == "panda", rule
assert rule["config"]["group"] == "standard-personal", rule
assert rule["config"]["api"] == {"type": "openai-chat-completions",
                                 "baseUrl": "https://api.example/v1"}, rule
assert rule["config"]["access"]["apiKey"] == "sekret-key", rule
assert rule["config"]["personalModelIds"] == ["injected-model"], rule
print(json.dumps({"sessionId": "sess-zc", "response": "zcode answer",
                  "usage": {"inputTokens": 5, "outputTokens": 2,
                            "totalTokens": 7}}))
''',
            env={
                "OPENPANDA_INJECTED_MODEL": "1",
                "ZCODE_BASE_URL": "https://api.example/v1",
                "ZCODE_API_KEY": "sekret-key",
                "ZCODE_MODEL": "injected-model",
                "OPENPANDA_MODEL_API_TYPE": "openai",
            },
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "zcode answer")
        self.assertEqual(payload["session_id"], "sess-zc")
        self.assertEqual(payload["tokens"], 7)

    def test_zcode_plain_run_keeps_its_own_config(self):
        # No injection → no ZCODE_PERSONAL_PROVIDER_CONFIG_FILE override, so
        # the CLI keeps using the model the operator picked in its TUI.
        payload, _, _ = run_adapter(
            "zcode.py", "zcode", r'''
import os, sys
assert sys.argv[1] == "--prompt", sys.argv
assert "ZCODE_PERSONAL_PROVIDER_CONFIG_FILE" not in os.environ
print("own model")
''',
        )
        self.assertTrue(payload["ok"], payload)
        self.assertEqual(payload["result"], "own model")

    def test_codex_timeout_is_reported(self):
        payload, _, _ = run_adapter(
            "codex.py", "codex", r'''
import time
time.sleep(10)
''',
            # Same as the generic watchdog test: timeout_s must stay small
            # here or the adapter would outwait the 8s outer bound.
            extra_request={"timeout_s": 2},
            timeout=8,
        )
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 124)


class HarnessContractTest(unittest.TestCase):
    """Direct contracts for the shared runtime adapters/_harness.py."""

    def test_prompt_argv_guards_a_leading_dash(self):
        # A prompt that opens with "-" is a flag to every CLI that takes the
        # message positionally. The guard only fires when it is needed, so the
        # common case keeps the argv the per-adapter tests pin.
        payload, _ = run_harness(
            "import json; _harness.emit(True, "
            "json.dumps([_harness.prompt_argv('fix it'), "
            "_harness.prompt_argv('- please fix')]), 0)"
        )
        self.assertEqual(json.loads(payload["result"]), [["fix it"], ["--", "- please fix"]])

    def test_resolve_binary_prefers_an_installed_alias(self):
        # Windows resolves executables through PATHEXT, so the fixture has to
        # carry the .cmd extension there; the function returns the candidate
        # name either way, which is what the caller spawns.
        payload, _ = run_harness(
            "import json, os, tempfile; "
            "d = tempfile.mkdtemp(); "
            "name = 'claude-code.cmd' if os.name == 'nt' else 'claude-code'; "
            "path = os.path.join(d, name); "
            "open(path, 'w').close(); "
            "os.chmod(path, 0o755); "
            "os.environ['PATH'] = d; "
            "_harness.emit(True, _harness.resolve_binary(['claude', 'claude-code']), 0)"
        )
        self.assertEqual(payload["result"], "claude-code", payload)

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

    def test_read_request_carries_cmd_template(self):
        # The card's agents.<name>.command rides the request verbatim for
        # generic.py; absent it parses as "".
        req = {"prompt": "p", "cmd": "zcode --prompt {prompt}"}
        payload, _ = run_harness(
            "_harness.emit(True, _harness.read_request().cmd, 0)",
            stdin_data=json.dumps(req),
        )
        self.assertEqual(payload["result"], "zcode --prompt {prompt}", payload)
        payload, _ = run_harness(
            "_harness.emit(True, repr(_harness.read_request().cmd), 0)",
            stdin_data=json.dumps({"prompt": "p"}),
        )
        self.assertEqual(payload["result"], "''", payload)

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

    def test_emit_carries_session_dead(self):
        # The wire flag Go's one-shot fallback keys on: present only when the
        # session process is gone.
        payload, _ = run_harness(
            "_harness.emit(False, 'provider exploded', 1, session_dead=True)")
        self.assertIs(payload["session_dead"], True)
        payload, _ = run_harness("_harness.emit(True, 'done', 0)")
        self.assertNotIn("session_dead", payload)

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
            failcli = write_executable(tmp / "failcli", r'''
import sys
print("boom output")
sys.exit(3)
''')
            payload, _ = run_harness(
                "_harness.run_simple([%r])" % str(failcli))
            self.assertFalse(payload["ok"], payload)
            self.assertEqual(payload["exit_code"], 3)
            self.assertEqual(payload["result"], "boom output")

    def test_run_simple_falls_back_to_stderr_diagnosis(self):
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            errcli = write_executable(tmp / "errcli", r'''
import sys
sys.stderr.write("diagnosis text")
sys.exit(1)
''')
            payload, _ = run_harness(
                "_harness.run_simple([%r])" % str(errcli))
            self.assertFalse(payload["ok"], payload)
            self.assertEqual(payload["exit_code"], 1)
            self.assertEqual(payload["result"], "diagnosis text")

    def test_run_simple_missing_binary_is_reported(self):
        payload, _ = run_harness(
            "_harness.run_simple(['definitely-not-on-path-xyz'])")
        self.assertFalse(payload["ok"], payload)
        self.assertEqual(payload["exit_code"], 127)
        self.assertEqual(payload["result"], "definitely-not-on-path-xyz binary not found")

    def test_emit_event_frames(self):
        """emit_event writes a bounded {"type":"event"} NDJSON frame on
        stderr: known fields ride under their names, unknown keys land in
        "data", oversized input is clamped."""
        _, proc = run_harness(
            "_harness.emit_event('tool_use', id='t1', name='Bash', "
            "input={'command': 'x' * 20000}, extra_key='v'); "
            "_harness.emit_event('text', text='hello', parent='t1'); "
            "_harness.emit(True, 'done', 0)")
        frames = [json.loads(l) for l in proc.stderr.splitlines()
                  if l.startswith("{")]
        frames = [f for f in frames if f.get("type") == "event"]
        self.assertEqual(len(frames), 2, proc.stderr)
        self.assertEqual(frames[0]["ev"], "tool_use")
        self.assertEqual(frames[0]["id"], "t1")
        self.assertEqual(frames[0]["name"], "Bash")
        # Unknown fields pass through at the top level.
        self.assertEqual(frames[0]["extra_key"], "v")
        # An oversized input structure degrades to clamped JSON text.
        self.assertIsInstance(frames[0]["input"], str)
        self.assertLess(len(frames[0]["input"]), 5000)
        self.assertEqual(frames[1]["parent"], "t1")
        self.assertEqual(frames[1]["text"], "hello")

    def test_emit_event_clamps_multibyte_by_bytes(self):
        """Field limits are UTF-8 byte budgets: the Go harness spills any
        stderr line past 24KB to diagnostics, and 16000 CJK characters are
        ~48KB on the wire — a character-count clamp would emit a line too
        long to survive and the event would be dropped instead of clamped."""
        _, proc = run_harness(
            "_harness.emit_event('text', text='汉' * 16000); "
            "_harness.emit(True, 'done', 0)")
        frames = [json.loads(l) for l in proc.stderr.splitlines()
                  if l.startswith("{")]
        frames = [f for f in frames if f.get("type") == "event"]
        self.assertEqual(len(frames), 1, proc.stderr)
        self.assertTrue(frames[0]["text"].endswith("…[截断]"),
                        frames[0]["text"][-40:])
        # The whole stderr line stays under the Go side's line cap.
        self.assertLess(len(proc.stderr.encode("utf-8")), 24 * 1024)

    def test_emit_event_clamp_preserves_code_points(self):
        """Byte clamping must not split a multi-byte code point — the cut
        text still decodes cleanly and carries the truncation marker."""
        _, proc = run_harness(
            "import sys; "
            "print(_harness._clamp_str('汉' * 6000, 16000), file=sys.stderr)")
        # 6000 CJK chars ≈ 18000 bytes: clamped to ≤16000 bytes + marker.
        out = proc.stderr.strip()
        self.assertTrue(out.endswith("…[截断]"), out[-40:])
        self.assertLessEqual(len(out.encode("utf-8")), 16000 + 32)

    def test_run_simple_transcript_events(self):
        """A plain CLI run lands on the transcript as one tool_use/
        tool_result pair — argv shape on the call, bounded output on the
        result."""
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            okcli = write_executable(tmp / "okcli", r'''
import sys
print("cli output")
''')
            payload, proc = run_harness(
                "_harness.run_simple([%r, 'a1'], label='okcli')"
                % str(okcli))
            self.assertTrue(payload["ok"], payload)
            frames = [json.loads(l) for l in proc.stderr.splitlines()
                      if l.startswith("{")]
            frames = [f for f in frames if f.get("type") == "event"]
            self.assertEqual([f["ev"] for f in frames],
                             ["tool_use", "tool_result"])
            self.assertEqual(frames[0]["name"], "okcli")
            # The prompt lives inside argv — the event carries the arg
            # count, not the arguments. phase=spawn marks it as adapter
            # bookkeeping: the Go watchdog keeps the startup-grace window
            # open until a real event or raw stderr line arrives.
            self.assertEqual(frames[0]["input"], {"argc": 1})
            self.assertEqual(frames[0]["phase"], "spawn")
            self.assertEqual(frames[1]["tool_use_id"], "okcli")
            self.assertFalse(frames[1]["is_error"])
            self.assertEqual(frames[1]["content"], "cli output")

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
            treecli = write_executable(tmp / "treecli", r'''
import os, subprocess, sys, time
subprocess.Popen([sys.executable, os.environ["TREE_CHILD"]])
time.sleep(60)
''')
            payload, _ = run_harness(
                "_harness.run_simple([%r], timeout=2, label='treecli')"
                % str(treecli),
                env={"TREE_CHILD": str(source_path(tmp / "treechild")),
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


class ResolveArgvTest(unittest.TestCase):
    """Windows argv resolution for npm-installed agent CLIs.

    Runs on any host: sys.platform is patched to win32 inside the harness
    subprocess, so the win32 branch of _harness.resolve_argv is exercised
    without a Windows machine.
    """

    def _resolve(self, argv, env=None):
        body = ("import sys, json; sys.platform='win32'; "
                "print(json.dumps(_harness.resolve_argv(%r)))" % (argv,))
        payload, proc = run_harness(body, env=env)
        self.assertIsNotNone(payload, proc.stderr)
        return payload

    def test_posix_passthrough(self):
        # sys.platform untouched → the argv comes back byte-identical.
        # Only a POSIX host passes a bare command through: on Windows the
        # _shim_exe branch resolves it to the real executable, because
        # CreateProcess does not search PATH for a bare name the way a POSIX
        # shell does. The resolved shape is covered by the win32 cases below.
        if sys.platform == "win32":
            self.skipTest("Windows resolves a bare command to its real exe")
        payload, proc = run_harness(
            "import json; print(json.dumps(_harness.resolve_argv("
            "['opencode', 'run', 'hi'])))")
        self.assertIsNotNone(payload, proc.stderr)
        self.assertEqual(payload, ["opencode", "run", "hi"])

    def test_win32_prefers_sibling_exe(self):
        # APPDATA\npm\zzagent.exe beats zzagent.cmd: the real exe gets argv
        # fidelity (the .cmd's %* forwarding would mangle quoting).
        with tempfile.TemporaryDirectory() as td:
            npm = pathlib.Path(td) / "npm"
            npm.mkdir()
            (npm / "zzagent.cmd").write_text(
                '@echo off\r\n"%~dp0\\zzagent.exe" %*\r\n')
            (npm / "zzagent.exe").write_text("x")
            out = self._resolve(["zzagent", "run", "a prompt"],
                                env={"APPDATA": td})
            self.assertEqual(out[0], str(npm / "zzagent.exe"))
            self.assertEqual(out[1:], ["run", "a prompt"])

    def test_win32_shim_body_exe(self):
        # A .cmd that wraps a .exe deeper in the tree: the body's quoted
        # path is resolved through %~dp0.
        with tempfile.TemporaryDirectory() as td:
            npm = pathlib.Path(td) / "npm"
            real = npm / "node_modules" / "pkg" / "bin"
            real.mkdir(parents=True)
            (real / "real.exe").write_text("x")
            (npm / "zzagent.cmd").write_text(
                '@echo off\r\n"%~dp0\\node_modules\\pkg\\bin\\real.exe" %*\r\n')
            out = self._resolve(["zzagent", "go"], env={"APPDATA": td})
            self.assertEqual(out[0], str(real / "real.exe"))
            self.assertEqual(out[1:], ["go"])

    def test_win32_npm_cmdshim_runs_node_directly(self):
        # The npm cmd-shim shape — "%_prog%" "%dp0%\node_modules\...\bin\x"
        # %* — resolves to [node, script] so the spawn skips cmd.exe and
        # %* forwarding entirely: the requoting path that silently dropped
        # the task prompt on real Windows runs.
        with tempfile.TemporaryDirectory() as td:
            npm = pathlib.Path(td) / "npm"
            script = npm / "node_modules" / "pkg" / "bin" / "cli"
            script.parent.mkdir(parents=True)
            script.write_text("// js\n")
            (npm / "node.exe").write_text("x")
            (npm / "zzagent.cmd").write_text(
                "@ECHO off\r\n"
                'IF EXIST "%dp0%\\node.exe" (\r\n'
                '  SET "_prog=%dp0%\\node.exe"\r\n'
                ") ELSE (\r\n"
                '  SET "_prog=node"\r\n'
                ")\r\n"
                'endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & '
                '"%_prog%"  "%dp0%\\node_modules\\pkg\\bin\\cli" %*\r\n')
            out = self._resolve(["zzagent", "run", "a prompt"],
                                env={"APPDATA": td})
            self.assertEqual(out[0], str(npm / "node.exe"))
            self.assertEqual(out[1], str(script))
            self.assertEqual(out[2:], ["run", "a prompt"])

    def test_win32_handwritten_interpreter_pair(self):
        # A hand-written wrapper (no npm shim involved) names the interpreter
        # first and the script second with absolute paths:
        #   "C:\...\node.exe" "C:\...\zcode.cjs" %*
        # Resolving only the .exe would drop the script and hand the CLI's
        # flags to node itself — which is exactly how zcode died with
        # "bad option: --prompt" while looking like an adapter bug.
        with tempfile.TemporaryDirectory() as td:
            npm = pathlib.Path(td) / "npm"
            npm.mkdir()
            interp = npm / "node.exe"
            interp.write_text("x")
            script = npm / "cli.cjs"
            script.write_text("// js\n")
            (npm / "zzagent.cmd").write_text(
                '@echo off\r\n"%s" "%s" %%*\r\n' % (interp, script))
            out = self._resolve(["zzagent", "--prompt", "a prompt"],
                                env={"APPDATA": td})
            self.assertEqual(out[0], str(interp))
            self.assertEqual(out[1], str(script))
            self.assertEqual(out[2:], ["--prompt", "a prompt"])

    def test_win32_launcher_shim_resolves_script_pair(self):
        # The shape the contract suite writes on Windows: a .cmd launcher
        # naming the interpreter and the script. Both halves resolve, so the
        # fake CLI is spawned directly instead of through cmd.exe — whose %*
        # forwarding would requote the prompt.
        with tempfile.TemporaryDirectory() as td:
            npm = pathlib.Path(td) / "npm"
            npm.mkdir()
            python = npm / "python.exe"
            python.write_text("x")
            with mock.patch.object(sys, "platform", "win32"), \
                    mock.patch.object(sys, "executable", str(python)):
                write_executable(npm / "zzfake", "print('hi')\n")
            self.assertTrue((npm / "zzfake.py").is_file())
            out = self._resolve(["zzfake", "run", "a prompt"],
                                env={"APPDATA": td})
            self.assertEqual(out[0], str(python))
            self.assertEqual(out[1], str(npm / "zzfake.py"))
            self.assertEqual(out[2:], ["run", "a prompt"])

    def test_win32_cmdshim_via_comspec(self):
        # A .cmd with no discoverable .exe falls back to COMSPEC /c with
        # list2cmdline quoting so the shim receives the args intact.
        with tempfile.TemporaryDirectory() as td:
            npm = pathlib.Path(td) / "npm"
            npm.mkdir()
            (npm / "zzagent.cmd").write_text("@echo off\r\nnode x %*\r\n")
            out = self._resolve(["zzagent", "run", "two words"],
                                env={"APPDATA": td})
            self.assertTrue(out[0].lower().endswith("cmd.exe"), out)
            self.assertEqual(out[1:4], ["/d", "/s", "/c"])
            self.assertIn("zzagent.cmd", out[4])
            self.assertIn('"two words"', out[4])

    def test_win32_missing_binary_stays_honest(self):
        # Nothing on PATH or the probed dirs → the original argv survives
        # so the spawn raises a truthful FileNotFoundError (exit 127).
        with tempfile.TemporaryDirectory() as td:
            out = self._resolve(["definitely-missing-cli-xyz", "run"],
                                env={"APPDATA": td})
            self.assertEqual(out, ["definitely-missing-cli-xyz", "run"])


if __name__ == "__main__":
    unittest.main()
