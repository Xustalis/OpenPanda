#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: OpenCode CLI → PANDA Commander.

Protocol (shared with claude_code.py / codex.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus optional {usage, session_id}
  stderr: NDJSON {"type":"progress","note":str,"kind":str?} status lines,
          plus {"type":"event","ev":...} transcript frames — a text part on
          first sighting → text, a tool part → tool_use on first sighting
          and tool_result on its first terminal status; see _harness.py

Execution mode: `opencode run --format json` streams the session's raw JSON
events (one per line). Text parts accumulate into the answer, tool parts
become progress notes, and the message/session envelopes carry the token
usage, the cost and the session id a follow-up round resumes with
(--session <id>, so a supervision continuation keeps opencode's own
conversation instead of cold-starting). CLIs too old for --format json
degrade to the plain text mode. This adapter never prints secrets.

OpenCode is model-agnostic, and the free-tier roster rotates constantly —
between builds models are renamed, retired and replaced. So on every run the
adapter re-reads the install's live roster (`opencode models`, the same view
the TUI's /model shows) and, when nothing pins a model, picks the most
capable entry itself: `opencode api model.list` (v2 builds) exposes each
model's real metadata — context/output limits, tool support, modalities,
reasoning variants, cost — which drives a capability score; on builds whose
catalog probe fails the score falls back to slug heuristics (family, size and
stability markers). A paid entry only wins when it clearly outscores the free
ones — opencode is the low-cost tier in the mesh — and a simple task (the
request's low complexity score, or a light effort level) drops paid entries
to last resort: any qualified free model beats every paid one there. Set
OPENCODE_MODEL (or
ANTHROPIC_MODEL) to a provider/model id (e.g. deepseek/deepseek-chat) to pin
a custom model on both paths; a pinned id the roster no longer carries is
substituted with the closest served model (a progress note names the swap) or
refused with the roster in the diagnosis, because a stale id makes every run
die in seconds with an opaque resolution error. tools_policy is noted but not
enforced: opencode gates tools through its own permission config, not a CLI
whitelist, so both policies run with the agent's configured tool face.
restricted (unconsented remote task) narrows to the built-in read-only agent:
`--agent plan` denies file mutations and write-class bash commands while
read tools keep working, and --auto cannot lift it (it only auto-approves
permissions that are not explicitly denied). A build without --agent refuses
the run rather than degrading to a full-power face.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the opencode difference:
model resolution/selection, the command line and the event reduction.
"""
import atexit
import json
import os
import re
import subprocess
import tempfile

import _harness as harness

# opencode resolves --model as provider/model; a bare model name is not a valid
# provider and fails resolution. The built-in free model needs no key or config,
# so it is the default when no provider/model id is given. The list of models
# an install actually serves drifts (free tiers are renamed/retired), so this
# constant is only a starting point: _resolve_model re-anchors it against the
# live `opencode models` output before anything is passed on the command line.
DEFAULT_MODEL = "opencode/deepseek-v4-flash-free"

_FLAG_PROBE = None
_MODEL_LIST = None
_MODEL_CATALOG = None


def _cli_flags():
    """Probe `opencode run --help` once per process for the flags this build
    accepts.

    Flag names drift across builds: the permission flag shipped as
    --dangerously-skip-permissions before it was renamed --auto, and
    --format json arrived later. Passing a flag the build does not know
    makes yargs print its usage line instead of running the prompt — the
    failure that looked like a lost message ("run opencode with a message")
    on a real Windows 1.4.3 install while the adapter kept sending 1.18
    flags. An empty probe result keeps the current-build flags, the common
    case; the run itself then reports any real problem honestly."""
    global _FLAG_PROBE
    if _FLAG_PROBE is not None:
        return _FLAG_PROBE
    text = ""
    try:
        argv = harness.resolve_argv(["opencode", "run", "--help"])
        proc = subprocess.run(argv, capture_output=True, timeout=15)
        text = harness.decode_cli((proc.stdout or b"") + (proc.stderr or b""))
    except Exception:
        text = ""
    auto = ""
    if re.search(r"--auto\b", text):
        auto = "--auto"
    elif re.search(r"--dangerously-skip-permissions\b", text):
        auto = "--dangerously-skip-permissions"
    _FLAG_PROBE = {"format": bool(re.search(r"--format\b", text)), "auto": auto,
                   "agent": bool(re.search(r"--agent\b", text))}
    return _FLAG_PROBE


class ProviderFailure(Exception):
    """Provider or upstream API failure (e.g. rate limit, auth, server_error)."""


class Unsupported(Exception):
    """The CLI rejected --format json — degrade to plain text mode."""


_MODEL_LINE_RE = re.compile(r"^[\w.@+-]+/[\w.@+-]+$")


def _available_models():
    """The models this opencode install actually serves (`opencode models`),
    probed once per process.

    The CLI's free-tier roster drifts between builds — a hard-coded default
    like opencode/deepseek-v4-flash-free stops resolving the day the tier is
    renamed (deepseek-v4.1-…), and the run then dies in seconds with a yargs
    "model not found" that reads like a task failure. An empty list means the
    probe failed (old build without the subcommand, service down): callers
    keep their pre-probe behaviour rather than guessing."""
    global _MODEL_LIST
    if _MODEL_LIST is not None:
        return _MODEL_LIST
    models = []
    try:
        argv = harness.resolve_argv(["opencode", "models"])
        proc = subprocess.run(argv, capture_output=True, timeout=15)
        if proc.returncode == 0:
            for line in harness.decode_cli(proc.stdout or b"").splitlines():
                line = line.strip()
                if _MODEL_LINE_RE.match(line):
                    models.append(line)
    except Exception:
        models = []
    _MODEL_LIST = models
    return models


def _model_catalog():
    """Model metadata map from `opencode api model.list` (v2 builds), probed
    once per process.

    The catalog carries what the bare roster cannot: each served model's
    context/output limits, tool support, input modalities, reasoning
    variants, release time and cost — the fields the capability pick in
    _pick_model scores. An empty dict means the probe failed (pre-v2 build
    without the api subcommand, server unreachable, foreign output): callers
    then score on slug heuristics alone."""
    global _MODEL_CATALOG
    if _MODEL_CATALOG is not None:
        return _MODEL_CATALOG
    catalog = {}
    try:
        argv = harness.resolve_argv(["opencode", "api", "model.list"])
        proc = subprocess.run(argv, capture_output=True, timeout=15)
        if proc.returncode == 0:
            doc = json.loads(harness.decode_cli(proc.stdout or b""))
            for entry in doc.get("data") or []:
                if not isinstance(entry, dict):
                    continue
                key = "%s/%s" % (entry.get("providerID"), entry.get("id"))
                if "/" not in key.strip("/"):
                    continue
                catalog[key] = entry
    except Exception:
        catalog = {}
    _MODEL_CATALOG = catalog
    return catalog


# Slug heuristics for the capability pick — the fallback when the catalog
# probe fails, and the family/stability component of the score when it does
# not. Coding-proven families outrank general ones (best first); _SIZE_UP /
# _SIZE_DOWN name the tier markers vendors put in the slug; _UNSTABLE marks
# previews and alphas a production run should only take when nothing stable
# is served.
_FAMILIES = (
    "claude", "gpt", "codex", "grok", "kimi", "qwen", "deepseek", "glm",
    "nemotron", "minimax", "longcat", "gemini", "llama", "mistral",
)
_SIZE_UP = ("ultra", "super", "max", "pro", "opus", "large")
_SIZE_DOWN = ("nano", "mini", "lite", "lightning", "flash", "spark",
              "small", "tiny", "mimo", "haiku")
_UNSTABLE = ("alpha", "beta", "preview", "experimental", "contributor")

# Cost handling in the pick. A paid entry always carries a penalty — opencode
# sits in the mesh's low-cost tier — but a strong one may still win on a
# heavy task. On a simple task (low complexity score / light effort) the
# penalty becomes absolute: any qualified free model outranks every paid one.
_PAID_PENALTY = 10
_PAID_PENALTY_SIMPLE = 1e6
# Complexity (0–1, the entry classifier's score) below which a task counts
# as simple for the free-first pick. 0 means "never scored" and does not
# trigger the preference.
_SIMPLE_COMPLEXITY = 0.4
# Light effort levels mean the same thing.
_LIGHT_EFFORTS = ("low", "minimal", "none")


def _slug_score(model):
    """Capability heuristic from the model id alone: family rank, size tier,
    stability markers and a small recency-of-version nudge."""
    slug = model.split("/")[-1].lower()
    tokens = re.split(r"[-_.]+", slug)
    score = 0.0
    for i, fam in enumerate(_FAMILIES):
        if any(fam in t for t in tokens):
            score += (len(_FAMILIES) - i) * 2
            break
    if any(t in _SIZE_UP for t in tokens):
        score += 8
    if any(t in _SIZE_DOWN for t in tokens):
        score -= 8
    if any(t in _UNSTABLE for t in tokens):
        score -= 12
    versions = []
    for t in tokens:
        digits = t[1:] if t.startswith("v") else t
        if digits.isdigit():
            versions.append(int(digits))
    if versions:
        score += min(max(versions), 10) * 0.1
    return score


def _is_paid(model, catalog):
    """Whether serving this model bills per token.

    The catalog's cost entries answer directly when they exist. With no
    catalog coverage the provider id is the tell: opencode's own provider is
    the built-in free tier; anything else is a user-configured provider that
    may bill — assuming paid is the safe error direction for a free-first
    pick (a misjudged local provider still wins when no free model
    qualifies)."""
    entry = catalog.get(model)
    if entry is not None:
        return any(c.get("input") or c.get("output")
                   for c in entry.get("cost") or [] if isinstance(c, dict))
    return model.split("/")[0] != "opencode"


def _score_model(model, catalog):
    """Score one roster entry for agent work. Returns (score, variant_ids).

    A catalog entry can disqualify the model outright (disabled, retired, or
    no tool calling — an agent run without tools cannot act), otherwise its
    real limits and features refine the slug heuristic. Paid models take a
    penalty: opencode sits in the mesh's low-cost tier, so a paid entry must
    clearly outscore the free ones before the adapter spends on it."""
    score = _slug_score(model)
    entry = catalog.get(model)
    if entry is None:
        if _is_paid(model, catalog):
            score -= _PAID_PENALTY
        return score, []
    if entry.get("enabled") is False:
        return -1e9, []
    status = entry.get("status")
    if status is not None and status != "active":
        return -1e9, []
    caps = entry.get("capabilities") if isinstance(entry.get("capabilities"), dict) else {}
    if caps.get("tools") is False:
        return -1e9, []
    limit = entry.get("limit") if isinstance(entry.get("limit"), dict) else {}
    try:
        ctx = int(limit.get("context") or 0)
        out = int(limit.get("output") or 0)
    except (TypeError, ValueError):
        ctx = out = 0
    score += min(max(ctx, 0), 1_000_000) / 100_000
    score += min(max(out, 0), 500_000) / 100_000
    variants = [v.get("id") for v in entry.get("variants") or []
                if isinstance(v, dict) and isinstance(v.get("id"), str)]
    if any(v not in ("none", "minimal") for v in variants):
        score += 4
    if "image" in (caps.get("input") or []):
        score += 2
    if _is_paid(model, catalog):
        score -= _PAID_PENALTY
    return score, variants


def _pick_model(available, prefer_free=False):
    """The most capable model on the live roster. Returns (model, variants)
    — ("", []) when the roster is empty or every entry is disqualified.

    prefer_free is the simple-task mode: a paid model is no longer a
    contender but a last resort, so it only wins when nothing free
    qualifies."""
    catalog = _model_catalog()
    best, best_score, best_variants = "", -1e18, []
    for model in available:
        score, variants = _score_model(model, catalog)
        if prefer_free and _is_paid(model, catalog):
            score -= _PAID_PENALTY_SIMPLE
        if score > best_score:
            best, best_score, best_variants = model, score, variants
    if best_score <= -1e9:
        return "", []
    return best, best_variants


# What a rejected --model looks like on the wire: v2 answers a stale id with
# a provider.no-route error event ("Model unavailable: provider/id"), older
# builds with yargs resolution text. Matched against the failure text only —
# it must not catch an upstream overload that merely names the model.
_MODEL_FAILURE_RE = re.compile(
    r"model[ ._-]?(unavailable|not[ _]?found|unknown|invalid|unsupported|"
    r"does[ _]not[ _]exist|resolution)|"
    r"provider[._ ]?no[._-]?route|providermodelnotfound|no[ _]such[ _]model",
    re.I)


def _looks_like_model_failure(text):
    return bool(_MODEL_FAILURE_RE.search(text or ""))


def _fresh_pick(excluded, prefer_free):
    """Drop the probe caches, re-identify the served models and pick the
    best entry not in `excluded`. Returns _pick_model's (model, variants)."""
    global _MODEL_LIST, _MODEL_CATALOG
    _MODEL_LIST = None
    _MODEL_CATALOG = None
    available = [m for m in (_available_models() or sorted(_model_catalog()))
                 if m not in excluded]
    return _pick_model(available, prefer_free)


def _retry_pick(model, tried, prefer_free, effort, err_text):
    """A run rejected its model: the served set rotated between this
    dispatch's probe and the run itself (free tiers retire mid-flight).
    Re-identify the roster and return a fresh pick with the effort variant
    re-applied — "" when the failure is not a model-resolution error or
    nothing new qualifies. Bounded: gives up after a few rotations."""
    if not _looks_like_model_failure(err_text) or len(tried) >= 4:
        return ""
    tried.add(model.split("#")[0] if model else "")
    pick, variants = _fresh_pick(tried, prefer_free)
    if not pick:
        return ""
    if effort and effort in variants:
        pick += "#" + effort
    harness.progress("model %s rejected; re-probed roster, retrying on %s"
                     % (model or "default", pick))
    return pick


def _resolve_model(requested):
    """Anchor `requested` against the live model list.

    Returns (model, note, err):
      model — the provider/model id to pass; "" means pass no --model flag
      note  — a progress line when the request was substituted
      err   — a fatal diagnosis when nothing available can honour the request

    A failed probe or an empty request is a no-op (old behaviour). An exact
    match passes through. Otherwise the requested slug's tokens are matched
    against the live slugs (equal or prefix — "v4" matches "v4.1"); a full
    match is used with a progress note, and no match fails fast with the
    available roster instead of letting the CLI reject the model opaquely."""
    available = _available_models()
    if not available or not requested:
        return requested, "", ""
    if requested in available:
        return requested, "", ""
    want = [t for t in requested.split("/")[-1].split("-") if t]

    def matches(model):
        slug = [s for s in model.split("/")[-1].split("-") if s]
        return all(
            any(t == s or (len(t) >= 2 and len(s) >= 2 and (s.startswith(t) or t.startswith(s))) for s in slug)
            for t in want
        )

    full = [m for m in available if matches(m)]
    if full:
        provider = requested.split("/")[0]
        pick = next((m for m in full if m.split("/")[0] == provider), full[0])
        return pick, f"model {requested} is not available; using {pick}", ""
    return "", "", "opencode has no model matching " + requested + "; available models: " + ", ".join(available)


# The provider id panda's injected model rides under. opencode resolves
# --model from its OWN provider registry: OPENAI_BASE_URL/OPENAI_API_KEY are
# ignored (verified — the run dies with ProviderModelNotFoundError), and a
# model outside the build's catalog is never served. So an injected endpoint
# only becomes usable when the CLI is handed a provider of our own, which is
# what the fragment below does.
INJECTED_PROVIDER_ID = "panda"
INJECTED_CONFIG_ENV = "OPENCODE_CONFIG"

# Set once a provider fragment was actually written for this run. The plain
# path's rotate-and-retry has to tell two cases apart: panda pinned a model
# that this build already serves (a retry is legitimate), or panda handed us
# its own endpoint (a retry would silently swap the injection for a free
# model). OPENPANDA_INJECTED_MODEL=1 alone cannot say which.
_injected_provider = False


def _injected_provider_config(model):
    """Write the provider fragment pointing opencode at panda's endpoint.

    The endpoint and key ride env references ({env:...}) instead of the file:
    the config lands on disk, the secret does not. Returns the path, or "" when
    the injection carried no endpoint (nothing to point at).
    """
    base = (os.environ.get("OPENAI_BASE_URL") or "").strip()
    if not base or not model:
        return ""
    body = {
        "$schema": "https://opencode.ai/config.json",
        "provider": {
            INJECTED_PROVIDER_ID: {
                "npm": "@ai-sdk/openai-compatible",
                "name": "Panda",
                "options": {
                    "baseURL": "{env:OPENPANDA_INJECT_BASE_URL}",
                    "apiKey": "{env:OPENPANDA_INJECT_API_KEY}",
                },
                "models": {model: {"name": model}},
            }
        },
    }
    fd, path = tempfile.mkstemp(prefix="panda-opencode-", suffix=".json")
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        json.dump(body, fh)
    return path


def _drop(path):
    """Remove a temp file; cleanup must never fail an otherwise good run."""
    try:
        os.unlink(path)
    except OSError:
        pass


def main():
    global _injected_provider
    req = harness.read_request()
    prompt, timeout, cwd = req
    flags = _cli_flags()
    auto = [flags["auto"]] if flags["auto"] else []

    # Model resolution:
    # Always pass the build's permission flag so non-interactive runs
    # auto-approve tool permissions. Keep print-logs=false to avoid polluting
    # stdout with internal runtime diagnostics.
    base = ["opencode", "run", "--print-logs=true"] + auto
    if flags["format"]:
        base += ["--format", "json"]
    model = ""
    # injected marks a run whose model endpoint panda overrode. That model lives
    # on OUR provider (written above), not on the build's roster, so both roster
    # substitution and the rotate-and-retry below would hand the run back to a
    # free model — recording an injection that never reached the wire.
    injected = False
    if os.environ.get("OPENPANDA_INJECTED_MODEL") == "1":
        bare = (os.environ.get("OPENCODE_MODEL")
                or os.environ.get("OPENAI_MODEL", "")).strip()
        cfg = _injected_provider_config(bare)
        if cfg:
            os.environ["OPENPANDA_INJECT_BASE_URL"] = os.environ.get("OPENAI_BASE_URL", "")
            os.environ["OPENPANDA_INJECT_API_KEY"] = os.environ.get("OPENAI_API_KEY", "")
            os.environ[INJECTED_CONFIG_ENV] = cfg
            atexit.register(_drop, cfg)
            model = INJECTED_PROVIDER_ID + "/" + bare
            injected = True
            _injected_provider = True
        elif bare:
            # No endpoint came with the injection, so the request names a model
            # this build already serves: keep it. A bare name still needs the
            # openai/ provider every injected request used to get.
            model = bare if "/" in bare else f"openai/{bare}"
    else:
        model = os.environ.get("OPENCODE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
        # A bare name was never passed on this path (it cannot resolve as a
        # provider); keep that, and let the plain path's default cover it.
        if model and "/" not in model:
            model = ""

    # A simple task (low complexity score, or a light effort request)
    # prefers the provider's free tier: paid models only fill in when no
    # free entry qualifies.
    prefer_free = (0 < req.complexity < _SIMPLE_COMPLEXITY
                   or req.effort in _LIGHT_EFFORTS)

    # Identify the served model set on every dispatch, before --model is
    # passed: the free tier rotates between builds, so nothing about it is
    # carried over from the last run. `opencode models` gives the id list —
    # the same view the TUI's /model shows — and `opencode api model.list`
    # (v2) adds the capability metadata whose keys double as the roster
    # when the bare list fails.
    if injected:
        # The roster describes this build's own catalog, which says nothing
        # about the provider we just wrote — probing it only costs a call.
        available = []
    elif model:
        available = _available_models()
    else:
        available = _available_models() or sorted(_model_catalog())
    if available and not injected:
        harness.progress("identified served models: %s" % ", ".join(available))

    if model and not injected:
        # A request the roster does not carry is substituted with the
        # closest available id (progress-noted) or fails fast with the
        # roster, instead of an opaque seconds-long death.
        model, note, err = _resolve_model(model)
        if err:
            harness.emit(False, err, 1)
            return
        if note:
            harness.progress(note)
    elif injected:
        # The injected model is served by the provider we just wrote, not by
        # this build's catalog — resolving it against the roster could only
        # swap it for a free model and drop the injection.
        harness.progress("using injected model %s via %s" % (model, INJECTED_CONFIG_ENV))
    else:
        # Nothing pinned a model: pick the most capable entry off the live
        # roster instead of taking whatever default the build happens to
        # ship. An all-failed probe keeps the old behaviour: no --model,
        # the install's configured default.
        pick, variants = _pick_model(available, prefer_free)
        if pick:
            # A requested reasoning effort rides the model's own declared
            # variant (provider/model#variant); an undeclared or absent
            # request leaves the provider default.
            if req.effort and req.effort in variants:
                pick = pick + "#" + req.effort
            harness.progress(
                "auto-selected model %s (of %d served%s)" % (
                    pick, len(available), "; simple task prefers free" if prefer_free else ""))
            model = pick

    # A restricted request (unconsented remote task) narrows the run to the
    # built-in read-only agent: opencode's "plan" mode denies file mutations
    # and write-class bash commands while read tools keep working — verified
    # against v2 builds where ls passes and touch/redirects are refused.
    # --auto cannot lift it: it only auto-approves permissions NOT explicitly
    # denied, and the plan agent's denials are explicit. A build without
    # --agent cannot express the narrow face at all, so the run refuses —
    # degrading to the default agent would silently run full-power, the exact
    # hole restricted exists to close.
    agent_face = ""
    if req.restricted:
        if not flags["agent"]:
            harness.emit(False,
                         "restricted run refused: this opencode build has no "
                         "--agent flag to narrow the tool face", 1)
            return
        agent_face = "plan"
        harness.progress("restricted mode: --agent plan (read-only tool face)")

    def run_cmd(m):
        c = list(base)
        if m:
            c += ["--model", m]
        if agent_face:
            c += ["--agent", agent_face]
        # A follow-up round resumes the agent's own session: its reasoning
        # trail survives instead of cold-starting on the bare instruction.
        # `resumed` (not req.resume) is read here so an in-adapter retry —
        # the roster rotated mid-dispatch — continues the session this run
        # already minted rather than re-entering at the outer handle.
        if resumed:
            c += ["--session", resumed]
        c.append(prompt)
        return c

    if not flags["format"]:
        # A build too old for --format json goes straight to the plain text
        # mode — the stream attempt could only fail with a usage line.
        return _run_plain(prompt, cwd, timeout, req.resume, model, auto,
                          prefer_free, agent_face=agent_face)

    tried = set()
    resumed = req.resume
    while True:
        try:
            out = _run_events(run_cmd(model), cwd, timeout)
        except ProviderFailure as e:
            # A rejected --model means the roster rotated between this
            # run's probe and the run itself: re-identify and retry once
            # on the fresh pick before reporting the failure. An injected
            # model is exempt — a fresh pick would be a FREE model, quietly
            # replacing the endpoint panda just asked us to use.
            retry = "" if injected else _retry_pick(
                model, tried, prefer_free, req.effort, str(e))
            st = getattr(e, "state", None) or {}
            if retry:
                model = retry
                if st.get("session_id"):
                    resumed = st["session_id"]
                continue
            usage = st.get("usage") if isinstance(st.get("usage"), dict) else {}
            harness.emit(False, f"provider failure: {e}", 1,
                         tokens=(usage.get("input_tokens", 0) + usage.get("output_tokens", 0)) or None,
                         cost=st.get("cost"), usage=usage or None,
                         session_id=st.get("session_id") or None)
            return
        except Unsupported:
            # The help listed --format but the run rejected it: one-shot
            # plain text run. The plain command is rebuilt from scratch
            # (never token-filtered out of the run argv — the prompt itself
            # could match a filtered token) and carries the build's own
            # permission flag and a resolvable model.
            return _run_plain(prompt, cwd, timeout, resumed, model, auto,
                              prefer_free, tried, agent_face=agent_face)
        except subprocess.TimeoutExpired:
            harness.emit(False, "opencode timed out", 124)
            return
        except FileNotFoundError:
            harness.emit(False, "opencode binary not found", 127)
            return
        if not injected and not out["ok"] and _looks_like_model_failure(out["result"]):
            retry = _retry_pick(model, tried, prefer_free, req.effort,
                                out["result"])
            if retry:
                model = retry
                if out.get("session_id"):
                    resumed = out["session_id"]
                continue
        break

    harness.emit(out["ok"], out["result"], out["exit_code"],
                 tokens=out.get("tokens"), cost=out.get("cost"),
                 usage=out.get("usage"), session_id=out.get("session_id"))


def _run_plain(prompt, cwd, timeout, resume, model, auto,
               prefer_free=False, tried=None, agent_face=""):
    """One-shot plain text mode for builds without --format json."""
    # An injected run's model lives on panda's provider, not on this build's
    # roster: the rotate-and-retry below would swap in a free model and drop
    # the injection, so it is skipped entirely. A pinned model that this build
    # still serves is not that case, and keeps the retry.
    injected = _injected_provider
    if not model:
        # The built-in default rots as the free roster drifts; re-anchor it
        # against the live list too (probe failure keeps the constant).
        resolved, note, err = _resolve_model(DEFAULT_MODEL)
        if resolved:
            model = resolved
        elif err:
            # Nothing matches the built-in default either: any served model
            # beats a guaranteed resolution failure on the one-shot path.
            available = _available_models()
            if available:
                model = available[0]
                note = f"model {DEFAULT_MODEL} is not available; using {model}"
        if note:
            harness.progress(note)
    plain = ["opencode", "run", "--print-logs=false"] + auto + ["--model", model]
    if agent_face:
        plain += ["--agent", agent_face]
    if resume:
        plain += ["--session", resume]
    plain.append(prompt)
    try:
        returncode, text, err = harness.run_plain(plain, cwd=cwd, timeout=timeout)
    except subprocess.TimeoutExpired:
        harness.emit(False, "opencode timed out", 124)
        return
    except FileNotFoundError:
        harness.emit(False, "opencode binary not found", 127)
        return
    if not injected and returncode != 0 and _looks_like_model_failure("%s\n%s" % (err, text)):
        # Same roster-rotation retry as the stream path.
        tried = tried if tried is not None else set()
        if model:
            tried.add(model.split("#")[0])
        if len(tried) < 4:
            pick, _ = _fresh_pick(tried, prefer_free)
            if pick:
                harness.progress("model %s rejected; re-probed roster, "
                                 "retrying on %s" % (model, pick))
                return _run_plain(prompt, cwd, timeout, resume, pick, auto,
                                  prefer_free, tried, agent_face=agent_face)
    harness.emit(returncode == 0, text.strip() or err.strip(), returncode)


def _run_events(cmd, cwd, timeout):
    """Event mode: reduce opencode's raw JSON event stream to the result."""
    state = {
        "saw_event": False,
        "session_id": "",
        "texts": {},
        "order": [],
        "tool_outputs": [],
        "counted": set(),
        # Event-stream bookkeeping: parts re-emit as they update, so the
        # transcript emits each part once — a text part on first sighting,
        # a tool part's tool_use on first sighting and its tool_result the
        # first time it reports a terminal status.
        "seen_text": set(),
        "seen_tool": set(),
        "done_tool": set(),
        "usage": {"input_tokens": 0, "output_tokens": 0},
        "cost": None,
        "error": "",
    }

    def on_line(line):
        ev = harness.parse_json_line(line)
        if ev is None:
            return
        state["saw_event"] = True
        et = ev.get("type") or ""
        if et == "error":
            err_obj = ev.get("error") if isinstance(ev.get("error"), dict) else {}
            err_msg = ""
            if isinstance(err_obj.get("data"), dict):
                err_msg = err_obj["data"].get("message", "")
            if not err_msg:
                err_msg = str(err_obj.get("message") or "")
            state["error"] = err_msg or "opencode error"
            exc = ProviderFailure(state["error"])
            # The partial run state rides the exception so the terminal emit
            # can report the session/usage this failure leaves behind — the
            # orchestrator's retry resumes the session instead of
            # cold-starting and re-burning the whole run.
            exc.state = state
            raise exc
        _fold(ev, state)

    def on_stderr(chunk):
        low = chunk.lower()
        if "rate limit exceeded" in low or "ai_retryerror" in low or "ai_apicallerror" in low or "stream error" in low:
            exc = ProviderFailure(chunk.strip())
            exc.state = state
            raise exc

    returncode, err, timed_out = harness.run_stream(
        cmd, cwd=cwd, timeout=timeout, on_line=on_line, on_stderr=on_stderr)
    if timed_out:
        # The watchdog killed the process tree mid-run. What matters to the
        # orchestrator is not the corpse but the checkpoint: the session the
        # run already minted (a retry resumes it rather than re-exploring
        # from zero) and the tokens it already spent (the task's budget is
        # charged for them either way — hiding them would leak spend).
        usage = state["usage"]
        tokens = usage["input_tokens"] + usage["output_tokens"] or None
        return {
            "ok": False,
            "result": "opencode timed out",
            "exit_code": 124,
            "tokens": tokens,
            "cost": state["cost"],
            "usage": usage,
            "session_id": state["session_id"],
        }

    if not state["saw_event"]:
        # Nothing parsed came out: either the CLI rejects --format json
        # (older version — degrade) or it failed outright (surface stderr).
        low = err.lower()
        if returncode != 0 and ("unknown option" in low or "unknown argument" in low
                                or "unrecognized" in low or "invalid value" in low
                                or "unknown flag" in low):
            raise Unsupported()
        msg = err.strip() or f"opencode exited {returncode} without events"
        return {"ok": False, "result": msg, "exit_code": returncode or 1}

    text = "\n\n".join(state["texts"][pid] for pid in state["order"] if state["texts"].get(pid))
    if not text and state["tool_outputs"]:
        text = "\n\n".join(state["tool_outputs"])
    if not text:
        # Events streamed but no assistant text came out of them. Reporting this
        # as success (which is what returning `err` here used to do) is worse than
        # failing: the caller records a "✓ ok" task whose result is a wall of
        # stderr logs, the supervisor judge never sees an answer and re-runs the
        # agent until its budget is spent, and the task parks in review forever.
        # A failure instead hands the work to the next adapter in the chain.
        detail = err.strip().splitlines()
        tail = " / ".join(detail[-2:]) if detail else "no stderr"
        return {
            "ok": False,
            "result": ("opencode streamed events but no assistant text was extracted "
                       f"(unrecognized event schema); stderr tail: {tail}"),
            "exit_code": returncode or 1,
            "session_id": state["session_id"],
        }
    usage = state["usage"]
    tokens = usage["input_tokens"] + usage["output_tokens"] or None
    return {
        "ok": returncode == 0 and not state["error"],
        "result": text,
        "exit_code": returncode,
        "tokens": tokens,
        "cost": state["cost"],
        "usage": usage,
        "session_id": state["session_id"],
    }


def _session_id(v):
    """Accept a candidate session id only in opencode's own `ses…` shape.

    Session handles minted by another adapter (e.g. claude_code's UUIDs) or
    copied off an event envelope's `id` field are poison: passing one back via
    --session makes the CLI answer 400 and the run cold-starts, burning the
    full exploration again on every retry."""
    s = str(v or "")
    return s if s.startswith("ses") else ""


def _fold(ev, state):
    """Fold one opencode event into the running state (defensively: the
    event schema drifts between versions, so every access is shape-checked
    and unknown shapes are ignored, never fatal).

    Two envelope shapes are in the wild and both must work. Older builds nest
    the payload under "properties"; current builds (1.18.x, verified against a
    real stream) put `part`, `sessionID` and the rest directly on the event.
    Reading only "properties" is what silently produced no text at all: the
    parts were there, one level up, and the result fell through to stderr."""
    props = ev.get("properties") if isinstance(ev.get("properties"), dict) else ev
    et = ev.get("type") or ""

    # Session id: carried as sessionID on the envelope (current builds) or on
    # the part; a session.created envelope may nest it under info/id instead.
    # Only ses_-prefixed values are real session handles — opencode rejects
    # anything else server-side ("Expected a string starting with ses"), so a
    # blind adoption of the event envelope's own `id` (a UUID) used to mint a
    # poisoned handle: every later resume attempt then died on a 400 and
    # cold-started, re-burning the whole run.
    part = props.get("part") if isinstance(props.get("part"), dict) else None
    sid = _session_id(props.get("sessionID") or props.get("session_id"))
    if not sid and part is not None:
        sid = _session_id(part.get("sessionID") or part.get("session_id"))
    if not sid:
        info = props.get("info") if isinstance(props.get("info"), dict) else {}
        sid = _session_id(info.get("id") or props.get("id"))
    if sid and not state["session_id"]:
        state["session_id"] = sid
        # Announce the minted handle early, before the result envelope
        # exists: a run killed mid-stream (watchdog, stall, host hard
        # limit) still leaves a resumable checkpoint the Go side latched.
        harness.emit_event("session", id=sid)

    if part is not None:
        pid = str(part.get("id") or len(state["order"]))
        ptype = part.get("type")
        if ptype == "text" and isinstance(part.get("text"), str) and part["text"]:
            if pid not in state["texts"]:
                state["order"].append(pid)
                # Text parts re-emit as they grow; the transcript row shows
                # the first sighting. A mid-stream snapshot may be partial —
                # the final answer is the result either way, and one row per
                # part beats an update storm on the timeline.
                if pid not in state["seen_text"]:
                    state["seen_text"].add(pid)
                    harness.emit_event("text", text=part["text"], id=pid)
            state["texts"][pid] = part["text"]
        elif ptype == "tool":
            # The event that carries a tool part is "tool_use" on current builds
            # and "message.part.updated" (or similar …updated) on older ones.
            if et == "tool_use" or et.endswith("updated"):
                tool = str(part.get("tool") or "tool")
                st = part.get("state") if isinstance(part.get("state"), dict) else {}
                arg = str(st.get("title") or st.get("input") or "")[:80]
                # tool_use on first sighting — the transcript shows the call
                # while it runs; tool_result the first time a terminal
                # status lands (the part keeps re-emitting after that, so
                # everything below the check runs exactly once).
                if pid not in state["seen_tool"]:
                    state["seen_tool"].add(pid)
                    inp = st.get("input")
                    if isinstance(inp, str):
                        inp = {"input": inp} if inp else {}
                    harness.emit_event("tool_use", id=pid, name=tool,
                                       input=inp if isinstance(inp, dict) else {"input": inp})
                # The loose second test covers builds whose terminal status
                # does not live at state.status — but the tool's own input
                # text must not satisfy it, so it is pruned from the repr.
                drift = {k: v for k, v in st.items() if k != "input"}
                if st.get("status") in ("completed", "error") or "completed" in str(drift):
                    if pid not in state["done_tool"]:
                        state["done_tool"].add(pid)
                        harness.progress(f"{tool}: {arg}" if arg else tool)
                        out = st.get("output")
                        harness.emit_event(
                            "tool_result", tool_use_id=pid,
                            is_error=st.get("status") == "error",
                            content=str(out) if out is not None else "")
                        if isinstance(out, str) and out.strip():
                            state["tool_outputs"].append(out.strip())
        # Usage rides the step_finish part on current builds: each step reports
        # the tokens that call spent, so they accumulate (keyed by part id, so a
        # re-emitted part never double counts).
        self_tokens = part.get("tokens") if isinstance(part.get("tokens"), dict) else None
        if self_tokens and pid not in state["counted"]:
            state["counted"].add(pid)
            try:
                state["usage"]["input_tokens"] += int(self_tokens.get("input", 0) or 0)
                state["usage"]["output_tokens"] += int(self_tokens.get("output", 0) or 0)
            except (TypeError, ValueError):
                pass
        part_cost = part.get("cost")
        if isinstance(part_cost, (int, float)) and part_cost:
            state["cost"] = max(state["cost"] or 0, float(part_cost))
        return

    # Older builds hang usage/cost off the message envelope instead (cumulative
    # per message; keep the max so re-emits never double count).
    info = props.get("info") if isinstance(props.get("info"), dict) else props
    toks = info.get("tokens") if isinstance(info.get("tokens"), dict) else None
    if toks:
        try:
            state["usage"]["input_tokens"] = max(
                state["usage"]["input_tokens"], int(toks.get("input", 0)))
            state["usage"]["output_tokens"] = max(
                state["usage"]["output_tokens"], int(toks.get("output", 0)))
        except (TypeError, ValueError):
            pass
    cost = info.get("cost")
    if isinstance(cost, (int, float)) and cost:
        state["cost"] = max(state["cost"] or 0, float(cost))


if __name__ == "__main__":
    main()
