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
            "OPENPANDA_INJECTED_MODEL",
        ):
            merged.pop(key, None)
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
        self.assertEqual(kinds, ["tool_use", "thinking", "tool_result", "text"])
        self.assertEqual(events[0]["name"], "shell")
        self.assertEqual(events[0]["input"], {"command": "ls -la"})
        self.assertEqual(events[0]["id"], "i1")
        self.assertEqual(events[1]["thinking"], "pondering")
        self.assertEqual(events[2]["tool_use_id"], "i1")
        self.assertEqual(events[2]["content"], "total 0")
        self.assertFalse(events[2]["is_error"])
        self.assertEqual(events[3]["text"], "codex answer")
        self.assertEqual(events[3]["id"], "i3")

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
            "opencode.py", "opencode", r'''
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
            "opencode.py", "opencode", r'''
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
            "opencode.py", "opencode", r'''
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

    def test_opencode_textless_stream_fails_instead_of_reporting_success(self):
        """Events with no assistant text must be a failure, not a green result.

        Returning the stderr log wall as "ok" is what turned one broken run into
        a task that could never finish: the supervisor judge saw no answer, kept
        re-running the agent until the budget was spent, and parked the task in
        review. A failure hands the work to the next adapter in the chain.
        """
        payload, _, _ = run_adapter(
            "opencode.py", "opencode", r'''
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
# The plain path always carries a resolvable model: with no env model set it
# must be the built-in default, never the empty string.
assert "--model" in args
assert args[args.index("--model") + 1] == "opencode/deepseek-v4-flash-free"
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
import sys
args = sys.argv[1:]
assert args[args.index("--dir") + 1].endswith("/work"), args
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
            write_executable(tmp / "okcli", r'''
import sys
print("cli output")
''')
            payload, proc = run_harness(
                "_harness.run_simple([%r, 'a1'], label='okcli')"
                % str(tmp / "okcli"))
            self.assertTrue(payload["ok"], payload)
            frames = [json.loads(l) for l in proc.stderr.splitlines()
                      if l.startswith("{")]
            frames = [f for f in frames if f.get("type") == "event"]
            self.assertEqual([f["ev"] for f in frames],
                             ["tool_use", "tool_result"])
            self.assertEqual(frames[0]["name"], "okcli")
            # The prompt lives inside argv — the event carries the arg
            # count, not the arguments.
            self.assertEqual(frames[0]["input"], {"argc": 1})
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
