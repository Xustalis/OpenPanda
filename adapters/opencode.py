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
adapter re-reads the install's live roster (`opencode models` on older
builds, `opencode api model.list` on v2 where the bare list can be empty)
and, when nothing pins a model, picks the most capable free entry itself: the
catalog exposes each model's real metadata — context/output limits, tool
support, modalities, reasoning variants, cost — which drives a capability
score; on builds whose catalog probe fails the score falls back to slug
heuristics (family, size and stability markers). Both probes are cached
across adapter processes (keyed by the CLI's and config's fingerprints, with
a short TTL), so a burst of tasks pays the roster once; a rejected model
forces a fresh probe instead of trusting the cache. Free is the default tier
— opencode is the low-cost tier in the mesh, and auto-selecting a paid model
is how a task bills an account that cannot pay for it. A paid entry is chosen
automatically only with OPENPANDA_OPENCODE_ALLOW_PAID=1, and even then a
simple task (low complexity, light effort) demotes every paid entry to last
resort. A billing/quota rejection of a paid model retries once on the best
free model instead of ending the run. Models under the `opencode-go`
subscription provider are disqualified when no credential is stored, because
they die on "Go requires a credential". An operator who wants a specific
model, or a shortlist of them, sets routing.agent_models for this agent;
panda delivers the ordered list as OPENPANDA_AGENT_MODELS, and the first
entry the install actually serves and can authenticate wins — including a
paid or Go model, because naming it is an explicit choice. Entries that are
not served, lack a Go login, or cannot call tools are skipped with a progress
note, and the heuristic free-first pick covers the rest. Set OPENCODE_MODEL
(or
ANTHROPIC_MODEL) to a provider/model id (e.g. deepseek/deepseek-chat) to pin
a custom model on both paths; a pinned id the roster no longer carries is
substituted with the closest served model (a progress note names the swap) or
refused with the roster in the diagnosis, because a stale id makes every run
die in seconds with an opaque resolution error.

When panda injects its own model, the adapter receives a dialect-neutral
contract (OPENPANDA_MODEL_BASE_URL/API_KEY/NAME/API_TYPE) and writes a
provider fragment into a temporary OPENCODE_CONFIG. The fragment is merged
over the user's native config so MCP servers and permissions survive, uses
@ai-sdk/anthropic or @ai-sdk/openai-compatible according to the dialect, and
the run takes --standalone because a v2 background service would keep the
config it started with and answer "Model unavailable". tools_policy is noted
but not enforced: opencode gates tools through its own permission config, not
a CLI whitelist, so both policies run with the agent's configured tool face.
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
from urllib.parse import urlsplit

import _harness as harness

# opencode resolves --model as provider/model; a bare model name is not a valid
# provider and fails resolution. The built-in free model needs no key or config,
# so it is the default when no provider/model id is given. The list of models
# an install actually serves drifts (free tiers are renamed/retired), so this
# constant is only a starting point: _resolve_model re-anchors it against the
# live roster/catalog before anything is passed on the command line.
DEFAULT_MODEL = "opencode/deepseek-v4.1-flash-free"

_FLAG_PROBE = None
_MODEL_LIST = None
_MODEL_CATALOG = None

# Cross-process probe cache lifetimes. The roster and its metadata live on the
# service side and can change without any local signal, so they stay short; the
# flag set only moves with the installed build, whose fingerprint (path, size,
# mtime) is part of every cache key, so it can live much longer.
_MODELS_CACHE_TTL = 300
_CATALOG_CACHE_TTL = 600
_FLAGS_CACHE_TTL = 3600
_CACHE_IDENTITY = None


def _opencode_paths(leaf):
    """Candidate locations of one opencode state file.

    XDG (~/.config/opencode) covers macOS and Linux; a Windows install keeps
    the same file under %APPDATA%\\opencode or %LOCALAPPDATA%\\opencode, and a
    node there must not lose its config merge or Go-login detection.
    """
    home = os.path.expanduser("~")
    paths = [os.path.join(home, ".config", "opencode", leaf)]
    for env in ("APPDATA", "LOCALAPPDATA"):
        base = (os.environ.get(env) or "").strip()
        if base:
            paths.append(os.path.join(base, "opencode", leaf))
    return paths


def _cache_identity():
    """Everything that makes this install's roster answer unique.

    The CLI binary fingerprint catches an upgrade; the config files (the
    ambient one plus any OPENCODE_CONFIG override) catch a provider the user
    just added. Any empty fingerprint contributes nothing, and an all-empty
    identity disables the cache rather than sharing one global answer. Memoized
    per process: three probes in one run must all key on the same identity.
    """
    global _CACHE_IDENTITY
    if _CACHE_IDENTITY is not None:
        return _CACHE_IDENTITY
    parts = [harness.which_fingerprint("opencode")]
    cfg = os.environ.get("OPENCODE_CONFIG", "").strip()
    if cfg:
        parts.append(harness.file_fingerprint(cfg))
    for leaf in ("opencode.json", "opencode.jsonc"):
        for path in _opencode_paths(leaf):
            parts.append(harness.file_fingerprint(path))
    _CACHE_IDENTITY = "|".join(parts)
    return _CACHE_IDENTITY


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
    identity = _cache_identity()
    cached = harness.cache_get("opencode-flags", identity, _FLAGS_CACHE_TTL)
    if isinstance(cached, dict) and "standalone" in cached:
        # A cache written before the standalone probe is stale, not a
        # build without the flag: re-probe instead of pinning every run to
        # the old schema's answer for the whole TTL.
        auto = cached.get("auto") if cached.get("auto") in (
            "--auto", "--dangerously-skip-permissions") else ""
        _FLAG_PROBE = {"format": bool(cached.get("format")),
                       "auto": auto, "agent": bool(cached.get("agent")),
                       "standalone": bool(cached.get("standalone"))}
        return _FLAG_PROBE
    text = ""
    ok = False
    try:
        argv = harness.resolve_argv(["opencode", "run", "--help"])
        proc = subprocess.run(argv, capture_output=True, timeout=15)
        ok = proc.returncode == 0
        text = harness.decode_cli((proc.stdout or b"") + (proc.stderr or b""))
    except Exception:
        text = ""
    auto = ""
    if re.search(r"--auto\b", text):
        auto = "--auto"
    elif re.search(r"--dangerously-skip-permissions\b", text):
        auto = "--dangerously-skip-permissions"
    _FLAG_PROBE = {"format": bool(re.search(r"--format\b", text)), "auto": auto,
                   "agent": bool(re.search(r"--agent\b", text)),
                   # v2 runs through a background service that keeps the
                   # config it started with; an injected provider only takes
                   # effect when the run owns its server.
                   "standalone": bool(re.search(r"--standalone\b", text))}
    # Only a successful probe is an answer worth caching. A failed one prints
    # yargs usage or a stack trace, which reads as "this build has none of
    # these flags" — pinning an hour of runs to the plain-text fallback and
    # refusing restricted tasks with a diagnosis nobody ever observed.
    if ok and text.strip():
        harness.cache_put("opencode-flags", identity, _FLAG_PROBE)
    return _FLAG_PROBE


class ProviderFailure(Exception):
    """Provider or upstream API failure (e.g. rate limit, auth, server_error)."""


class Unsupported(Exception):
    """The CLI rejected --format json — degrade to plain text mode."""


_MODEL_LINE_RE = re.compile(r"^[\w.@+-]+/[\w.@+-]+$")


def _available_models(force=False):
    """The models this opencode install actually serves (`opencode models`),
    probed once per process and cached across processes.

    The CLI's free-tier roster drifts between builds — a hard-coded default
    like opencode/deepseek-v4-flash-free stops resolving the day the tier is
    renamed (deepseek-v4.1-…), and the run then dies in seconds with a yargs
    "model not found" that reads like a task failure. An empty list means the
    probe failed (old build without the subcommand, service down): callers
    keep their pre-probe behaviour rather than guessing.

    force skips both the in-process memo and the disk cache — the
    rotate-and-retry path needs what the service serves *now*."""
    global _MODEL_LIST
    if not force and _MODEL_LIST is not None:
        return _MODEL_LIST
    identity = _cache_identity()
    if not force:
        cached = harness.cache_get("opencode-models", identity, _MODELS_CACHE_TTL)
        if isinstance(cached, list):
            models = [m for m in cached
                      if isinstance(m, str) and _MODEL_LINE_RE.match(m)]
            if models:
                _MODEL_LIST = models
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
    # Only a real roster is worth keeping: caching an empty probe would hide a
    # service that came back, or a config the user just fixed, for its TTL.
    if models:
        harness.cache_put("opencode-models", identity, models)
    return models


def _model_catalog(force=False):
    """Model metadata map from `opencode api model.list` (v2 builds), probed
    once per process and cached across processes.

    The catalog carries what the bare roster cannot: each served model's
    context/output limits, tool support, input modalities, reasoning
    variants, release time and cost — the fields the capability pick in
    _pick_model scores. An empty dict means the probe failed (pre-v2 build
    without the api subcommand, server unreachable, foreign output): callers
    then score on slug heuristics alone. force bypasses both caches for the
    same reason as _available_models."""
    global _MODEL_CATALOG
    if not force and _MODEL_CATALOG is not None:
        return _MODEL_CATALOG
    identity = _cache_identity()
    if not force:
        cached = harness.cache_get("opencode-catalog", identity, _CATALOG_CACHE_TTL)
        if isinstance(cached, dict):
            catalog = {k: v for k, v in cached.items()
                       if isinstance(v, dict) and "/" in k.strip("/")}
            if catalog:
                _MODEL_CATALOG = catalog
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
                pid, mid = entry.get("providerID"), entry.get("id")
                if not isinstance(pid, str) or not pid.strip():
                    continue
                if not isinstance(mid, str) or not mid.strip():
                    continue
                key = "%s/%s" % (pid.strip(), mid.strip())
                catalog[key] = entry
    except Exception:
        catalog = {}
    _MODEL_CATALOG = catalog
    if catalog:
        harness.cache_put("opencode-catalog", identity, catalog)
    return catalog


def _served_models(force=False):
    """Every model this install can serve, from both probe surfaces.

    `opencode models` is the historical roster, but v2 builds answer it with
    an empty list while `opencode api model.list` carries the full catalog —
    on such a build the bare roster alone made every pinned id look
    unresolvable and left the pick with nothing to choose. The union keeps
    older builds (catalog absent) and newer ones (roster empty) working.
    """
    models = list(_available_models(force=force))
    seen = set(models)
    for model in sorted(_model_catalog(force=force)):
        if model not in seen:
            seen.add(model)
            models.append(model)
    return models


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


def _paid_allowed():
    """Whether an unpinned run may auto-select a paid model.

    Auto-picking the most capable entry out of the box is how a run bills a
    model the account cannot pay for: on an install with no Go subscription
    and an exhausted free tier, the pick landed on a paid flagship and every
    task died with "Insufficient account funds". Free models are the default
    for opencode — the mesh's low-cost tier — and spending money is an
    explicit opt-in (OPENPANDA_OPENCODE_ALLOW_PAID=1).
    """
    for name in ("OPENPANDA_OPENCODE_ALLOW_PAID", "OPENCODE_ALLOW_PAID"):
        if os.environ.get(name, "").strip().lower() in ("1", "true", "yes", "on"):
            return True
    return False


_GO_CREDENTIAL = None


def _has_go_credential():
    """Whether the install carries a credential for the Go/Zen provider.

    `opencode-go` is the subscription roster; without a stored login its
    models fail with "Go requires a credential". OPENCODE_API_KEY is the
    provider's own env contract; otherwise auth.json maps provider ids to
    credentials, and only an opencode/opencode-go entry counts — the file
    existing for some other vendor says nothing about this roster.
    """
    global _GO_CREDENTIAL
    if _GO_CREDENTIAL is None:
        found = bool(os.environ.get("OPENCODE_API_KEY", "").strip())
        home = os.path.expanduser("~")
        auth_paths = [os.path.join(home, ".local", "share", "opencode", "auth.json")]
        auth_paths += _opencode_paths("auth.json")
        for path in auth_paths:
            try:
                with open(path, "r", encoding="utf-8") as fh:
                    data = json.load(fh)
            except (OSError, ValueError):
                continue
            if not isinstance(data, dict):
                continue
            for key in ("opencode", "opencode-go"):
                val = data.get(key)
                if (isinstance(val, dict) and val) or \
                        (isinstance(val, str) and val.strip()):
                    found = True
                    break
            if found:
                break
        _GO_CREDENTIAL = found
    return _GO_CREDENTIAL


def _preferred_models():
    """The user's ordered model preference list, from routing.agent_models.

    panda ships it as a JSON array; a comma-separated value is accepted too
    for hand-run debugging. Duplicates are dropped, order is preserved.
    """
    raw = (os.environ.get("OPENPANDA_AGENT_MODELS") or "").strip()
    if not raw:
        return []
    try:
        doc = json.loads(raw)
    except ValueError:
        doc = None
    values = doc if isinstance(doc, list) else raw.split(",")
    out = []
    for value in values:
        item = str(value).strip()
        if item and item not in out:
            out.append(item)
    return out


def _preferred_model(available, catalog, exclude=()):
    """The first configured preference this install can actually serve.

    Returns (model, variants, notes). model is "" when no preference is
    configured or none qualifies; notes explain each skip so a typo or a
    missing Go login is visible in the progress stream instead of being
    silently ignored and replaced by the default pick.

    A preference wins over the free-first default: naming a paid model is an
    explicit choice, and the same list is how a Go subscriber selects the
    subscription roster. exclude lists models this run already tried, so a
    retry walks on to the next entry instead of returning the one that just
    failed.
    """
    prefs = _preferred_models()
    if not prefs:
        return "", [], []
    notes = []
    excluded = {m.split("#")[0] for m in exclude if m}
    for pref in prefs:
        want, _, variant = pref.partition("#")
        want, variant = want.strip(), variant.strip()
        if want in available:
            candidates = [want]
        elif "/" not in want:
            # A bare slug is matched against the served model ids, so
            # `kimi-k3` works without spelling the provider prefix. Both
            # providers may serve it; try each until one is usable.
            candidates = [m for m in available
                          if m.split("/")[-1].lower() == want.lower()]
        else:
            candidates = []
        candidates = [m for m in candidates if m not in excluded]
        if not candidates:
            notes.append("preferred model %s is not served by this install" % pref)
            continue
        for model in candidates:
            if model.split("/")[0] == "opencode-go" and not _has_go_credential():
                notes.append("preferred model %s needs an OpenCode Go credential "
                             "(run `opencode auth login`)" % model)
                continue
            score, variants = _score_model(model, catalog)
            if score <= -1e9:
                notes.append("preferred model %s is not usable for agent work" % model)
                continue
            if variant:
                return model + "#" + variant, variants, notes
            return model, variants, notes
    return "", [], notes


def _preferred_fresh(available):
    """Preference resolution plus the roster it resolved against.

    A configured preference is an explicit instruction. When the cached
    snapshot cannot find any entry, re-read the live roster before falling
    back to the heuristic pick — the same rule _resolve_model applies to a
    pinned id, and the reason a preference added after the cache was written
    used to look "not served" for its whole TTL. The returned list is the one
    the caller should keep using, so the fallback pick sees the same fresh
    roster as the preference lookup.
    """
    preferred, variants, notes = _preferred_model(available, _model_catalog())
    if preferred or not _preferred_models():
        return preferred, variants, notes, available
    global _MODEL_LIST, _MODEL_CATALOG
    _MODEL_LIST = None
    _MODEL_CATALOG = None
    fresh = _served_models(force=True)
    preferred, variants, notes = _preferred_model(fresh, _model_catalog(force=True))
    return preferred, variants, notes, fresh


def _is_paid(model, catalog):
    """Whether serving this model bills per token.

    The catalog's cost entries answer directly when they exist. Without
    catalog coverage only an explicit -free id counts as free: guessing from
    the provider id is how a paid flagship under opencode's own provider was
    read as the free tier and picked for every run.
    """
    entry = catalog.get(model)
    if entry is not None:
        return any(c.get("input") or c.get("output")
                   for c in entry.get("cost") or [] if isinstance(c, dict))
    return not model.split("/")[-1].lower().endswith("-free")


def _looks_like_funds_failure(text):
    """A run rejected the model for billing or quota reasons.

    These are not roster rotations: retrying the same paid model cannot
    succeed, but a free model (or panda's injected endpoint) can.
    """
    low = (text or "").lower()
    markers = (
        "insufficient account funds", "insufficient funds", "payment required",
        "freeusagelimiterror", "free usage limit", "out of credit",
        "credit balance", "go requires a credential", "quota",
    )
    return any(marker in low for marker in markers)


def _score_model(model, catalog):
    """Score one roster entry for agent work. Returns (score, variant_ids).

    A catalog entry can disqualify the model outright (disabled, retired, or
    no tool calling — an agent run without tools cannot act), otherwise its
    real limits and features refine the slug heuristic. Paid models take a
    penalty: opencode sits in the mesh's low-cost tier, so a paid entry must
    clearly outscore the free ones before the adapter spends on it."""
    provider = model.split("/")[0]
    if provider == "opencode-go" and not _has_go_credential():
        # The subscription roster without a stored login: the run dies on
        # "Go requires a credential" before doing any work.
        return -1e9, []
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


def _retry_pick(model, tried, prefer_free, effort, err_text):
    """A run rejected its model: move on to the next model this run may use.

    The user's ordered preference list is consulted first — a failed entry
    walks on to the next one rather than dropping the whole list for the
    heuristic pick, which is what "ordered fallback" means. When the list is
    exhausted (or none is configured) the served set is re-identified and the
    best remaining entry picked: a rejected --model can also mean the roster
    rotated between this dispatch's probe and the run itself (free tiers
    retire mid-flight). Returns "" when the failure is not a model-resolution
    error or nothing new qualifies, and is bounded: gives up after a few
    rotations."""
    if not _looks_like_model_failure(err_text) or len(tried) >= 4:
        return ""
    tried.add(model.split("#")[0] if model else "")
    global _MODEL_LIST, _MODEL_CATALOG
    _MODEL_LIST = None
    _MODEL_CATALOG = None
    fresh = _served_models(force=True)
    pick, variants, notes = _preferred_model(fresh, _model_catalog(force=True), exclude=tried)
    if not pick:
        pick, variants = _pick_model([m for m in fresh if m not in tried], prefer_free)
    else:
        for note in notes:
            harness.progress(note)
    if not pick:
        return ""
    if effort and effort in variants and "#" not in pick:
        pick += "#" + effort
    harness.progress("model %s rejected; retrying on %s"
                     % (model or "default", pick))
    return pick


def _funds_retry(model, tried, available):
    """The next model to try after a billing/quota rejection.

    A configured preference list is the explicit answer to "which model may
    this cost" — the next entry that is not excluded wins, even when it bills
    (the operator named it). Without one, the first free model this install
    can serve is used; "" when none remains, and the orchestrator's own
    injection fallback then takes over."""
    tried.add((model or "").split("#")[0])
    catalog = _model_catalog()
    pick, _variants, _notes = _preferred_model(available, catalog, exclude=tried)
    if pick:
        return pick
    free = [m for m in available
            if m not in tried and not _is_paid(m, catalog)]
    if not free:
        return ""
    # _pick_model disqualifies unusable entries (an opencode-go model with no
    # credential scores below the floor) and returns "" when none qualifies.
    pick, _ = _pick_model(free, prefer_free=True)
    return pick


def _match_model(requested, available):
    """Match `requested` against a roster snapshot. Returns (model, note, err).

    An exact match passes through. Otherwise the requested slug's tokens are
    matched against the snapshot's slugs (equal or prefix — "v4" matches
    "v4.1"); a full match is used with a progress note, and no match returns
    the diagnosis instead of guessing."""
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


def _resolve_model(requested):
    """Anchor `requested` against the live model list.

    Returns (model, note, err):
      model — the provider/model id to pass; "" means pass no --model flag
      note  — a progress line when the request was substituted
      err   — a fatal diagnosis when nothing available can honour the request

    A failed probe or an empty request is a no-op (old behaviour). A miss in
    the cached roster is re-probed before it is believed: the cache is a
    snapshot, and "not in the snapshot" is not "not served" — a model that
    shipped after the snapshot used to kill every task outright for up to its
    TTL, because this runs before the run whose rejection would have forced a
    re-probe. A failed re-probe proves nothing, so the requested slug is
    passed through and the provider answers for itself."""
    available = _served_models()
    if not available or not requested:
        return requested, "", ""
    model, note, err = _match_model(requested, available)
    if not err:
        return model, note, ""
    fresh = _served_models(force=True)
    if not fresh:
        return requested, "", ""
    return _match_model(requested, fresh)


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


def _injected_endpoint():
    """The endpoint panda injected, in a dialect-neutral form.

    The registry contract carries panda's own variable names
    (OPENPANDA_MODEL_BASE_URL/API_KEY/NAME plus OPENPANDA_MODEL_API_TYPE), so
    the adapter no longer has to assume OpenAI: an Anthropic-dialect model
    (DeepSeek's Messages endpoint, a Claude relay) is just as valid. The
    older OPENAI_*/ANTHROPIC_* variables still work for a config written by
    an earlier build.
    """
    api_type = (os.environ.get("OPENPANDA_MODEL_API_TYPE") or "").strip().lower()
    base = (os.environ.get("OPENPANDA_MODEL_BASE_URL") or "").strip()
    key = (os.environ.get("OPENPANDA_MODEL_API_KEY") or "").strip()
    name = (os.environ.get("OPENPANDA_MODEL_NAME") or "").strip()
    if not name:
        name = (os.environ.get("OPENCODE_MODEL")
                or os.environ.get("OPENAI_MODEL")
                or os.environ.get("ANTHROPIC_MODEL", "")).strip()
    if not base:
        # Legacy contract only: an older build injected the CLI's own env
        # names and no panda-named endpoint. Require the matching key so an
        # ambient ANTHROPIC_BASE_URL in the parent shell (a developer running
        # this adapter by hand) is not mistaken for an injected endpoint.
        if (api_type == "anthropic" or (not api_type and os.environ.get("ANTHROPIC_API_KEY"))) \
                and os.environ.get("ANTHROPIC_BASE_URL") and os.environ.get("ANTHROPIC_API_KEY"):
            base = os.environ["ANTHROPIC_BASE_URL"].strip()
            key = key or os.environ["ANTHROPIC_API_KEY"].strip()
            api_type = api_type or "anthropic"
        elif os.environ.get("OPENAI_BASE_URL") and os.environ.get("OPENAI_API_KEY"):
            base = os.environ["OPENAI_BASE_URL"].strip()
            key = key or os.environ["OPENAI_API_KEY"].strip()
            api_type = api_type or "openai"
    return name, base, key, api_type or "openai"


def _strip_jsonc(text):
    """Remove // and /* */ comments and trailing commas from a JSONC file.

    opencode's native config is .jsonc (comments allowed); the standard
    library has no parser for it, and the strip only has to be good enough
    to merge one provider key without touching the user's other settings.
    """
    out, i, n = [], 0, len(text)
    in_str = esc = False
    while i < n:
        c = text[i]
        if in_str:
            out.append(c)
            if esc:
                esc = False
            elif c == "\\":
                esc = True
            elif c == '"':
                in_str = False
            i += 1
            continue
        if c == '"':
            in_str = True
            out.append(c)
            i += 1
            continue
        if c == "/" and i + 1 < n and text[i + 1] == "/":
            while i < n and text[i] != "\n":
                i += 1
            continue
        if c == "/" and i + 1 < n and text[i + 1] == "*":
            i += 2
            while i + 1 < n and not (text[i] == "*" and text[i + 1] == "/"):
                i += 1
            i += 2
            continue
        if c == ",":
            j = i + 1
            while j < n and text[j] in " \t\r\n":
                j += 1
            if j < n and text[j] in "}]":
                i += 1  # trailing comma: drop it
                continue
        out.append(c)
        i += 1
    return "".join(out)


def _native_config():
    """The user's opencode config as a dict, or {} when there is none.

    The injected fragment must not become the whole config: opencode treats
    OPENCODE_CONFIG as its global config file, so replacing it with only a
    provider block would drop the user's MCP servers, permissions and
    defaults for the duration of the run. Merging mirrors what cc-switch
    does with its per-app common config.
    """
    candidates = []
    override = (os.environ.get("OPENCODE_CONFIG") or "").strip()
    if override:
        candidates.append(override)
    candidates += _opencode_paths("opencode.json")
    candidates += _opencode_paths("opencode.jsonc")
    for path in candidates:
        try:
            with open(path, "r", encoding="utf-8") as fh:
                text = fh.read()
        except OSError:
            continue
        text = text.lstrip("\ufeff")
        for doc in (text, _strip_jsonc(text)):
            try:
                parsed = json.loads(doc)
            except ValueError:
                continue
            if isinstance(parsed, dict):
                return parsed
        return {}
    return {}


def _injected_base(base, api_type):
    """The base URL the SDK provider should receive.

    The OpenAI-compatible provider appends /chat/completions directly, so the
    base must carry the API version. OpenCode's harness form (/zen and
    /zen/go) does not; the Anthropic provider adds /v1/messages itself, so it
    keeps the shorthand. The entry client normalizes this on its own path,
    which is why the adapter cannot simply reuse its URL rules.
    """
    if api_type == "anthropic":
        return base
    path = base.rstrip("/")
    host = (urlsplit(path).hostname or "").lower()
    if host == "opencode.ai" or host.endswith(".opencode.ai"):
        for suffix in ("/zen", "/zen/go"):
            if path.endswith(suffix):
                return path + "/v1"
    return base


def _injected_provider_config(model, base, key, api_type):
    """Write the provider fragment pointing opencode at panda's endpoint.

    The endpoint and key ride env references ({env:...}) instead of the file:
    the config lands on disk, the secret does not. The fragment is merged over
    the user's native config so their MCP servers and permissions survive the
    run. Returns the path, or "" when the injection carried no endpoint
    (nothing to point at).
    """
    if not base or not model:
        return ""
    base = _injected_base(base.rstrip("/"), api_type)
    os.environ["OPENPANDA_MODEL_BASE_URL"] = base
    os.environ["OPENPANDA_MODEL_API_KEY"] = key
    os.environ["OPENPANDA_MODEL_NAME"] = model
    os.environ["OPENPANDA_MODEL_API_TYPE"] = api_type
    # Anthropic-dialect endpoints need the Anthropic SDK provider: the
    # OpenAI-compatible one sends a Chat Completions body to a Messages
    # endpoint and every call fails with an opaque 400.
    pkg = ("@ai-sdk/anthropic" if api_type == "anthropic"
           else "@ai-sdk/openai-compatible")
    body = _native_config()
    body.setdefault("$schema", "https://opencode.ai/config.json")
    providers = body.get("provider")
    if not isinstance(providers, dict):
        providers = {}
    existing = providers.get(INJECTED_PROVIDER_ID)
    models = existing.get("models") if isinstance(existing, dict) else None
    if not isinstance(models, dict):
        models = {}
    models[model] = {"name": model}
    providers[INJECTED_PROVIDER_ID] = {
        "npm": pkg,
        "name": "Panda",
        "options": {
            "baseURL": "{env:OPENPANDA_MODEL_BASE_URL}",
            "apiKey": "{env:OPENPANDA_MODEL_API_KEY}",
        },
        "models": models,
    }
    body["provider"] = providers
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
        bare, ibase, ikey, itype = _injected_endpoint()
        cfg = _injected_provider_config(bare, ibase, ikey, itype)
        if cfg:
            os.environ[INJECTED_CONFIG_ENV] = cfg
            atexit.register(_drop, cfg)
            model = INJECTED_PROVIDER_ID + "/" + bare
            injected = True
            _injected_provider = True
            if flags["standalone"]:
                # v2 dispatches to a long-lived background service that was
                # started with the user's config; OPENCODE_CONFIG only reaches
                # a run that owns its server, or every injected model answers
                # "Model unavailable: panda/<id>".
                base.append("--standalone")
            harness.progress("using injected model %s (%s)"
                             % (model, itype))
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

    # Free models are this agent's default tier. A paid model is selected
    # automatically only when the operator opted in; otherwise it takes a
    # large penalty and wins only if nothing free is served at all. A simple
    # task (low complexity, light effort) insists on free even then.
    prefer_free = (not _paid_allowed()
                   or 0 < req.complexity < _SIMPLE_COMPLEXITY
                   or req.effort in _LIGHT_EFFORTS)

    # Identify the served model set on every dispatch, before --model is
    # passed: the free tier rotates between builds, so nothing about it is
    # carried over from the last run. `opencode models` is the historical
    # roster; `opencode api model.list` (v2) adds the catalog and, on builds
    # where the bare list is empty, is the roster itself.
    if injected:
        # The catalog describes this build's own providers, which says
        # nothing about the provider we just wrote — probing it only costs a
        # call.
        available = []
    else:
        available = _served_models()
    if available and not injected:
        # A count, not the whole roster: v2 serves over a hundred ids, and
        # the full list buried every other progress line in the task log.
        count = len(available)
        harness.progress("identified %d model%s served by this install"
                         % (count, "" if count == 1 else "s"))

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
        # swap it for a free model and drop the injection. The progress line
        # naming the model and dialect was already emitted above.
        pass
    else:
        # The user's ordered preference list (routing.agent_models) wins over
        # the heuristic pick; an explicit choice may be a paid or Go model.
        preferred, pref_variants, notes, available = _preferred_fresh(available)
        if preferred:
            for note in notes:
                harness.progress(note)
            if req.effort and "#" not in preferred and req.effort in pref_variants:
                preferred += "#" + req.effort
            harness.progress("using preferred model %s" % preferred)
            model = preferred
        else:
            for note in notes:
                harness.progress(note)
            # Nothing pinned or preferred: pick the most capable entry off the
            # live roster instead of taking whatever default the build happens
            # to ship. An all-failed probe keeps the old behaviour: no
            # --model, the install's configured default.
            pick, variants = _pick_model(available, prefer_free)
            if pick:
                # A requested reasoning effort rides the model's own declared
                # variant (provider/model#variant); an undeclared or absent
                # request leaves the provider default.
                if req.effort and req.effort in variants:
                    pick = pick + "#" + req.effort
                harness.progress(
                    "auto-selected model %s (of %d served%s)" % (
                        pick, len(available), "; free tier first" if prefer_free else ""))
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
                          prefer_free, effort=req.effort, agent_face=agent_face)

    tried = set()
    resumed = req.resume
    # Cap the free-model fallback: an account with an exhausted free tier
    # would otherwise walk every free entry, each dying on the same quota
    # error, before the orchestrator's injection rescue gets the task.
    funds_tries = 0
    while True:
        try:
            out = _run_events(run_cmd(model), cwd, timeout)
        except ProviderFailure as e:
            # A rejected --model means the roster rotated between this
            # run's probe and the run itself: re-identify and retry once
            # on the fresh pick before reporting the failure. An injected
            # model is exempt — a fresh pick would be a FREE model, quietly
            # replacing the endpoint panda just asked us to use.
            #
            # A billing/quota rejection is a different animal: the model is
            # served and resolvable, the account simply cannot pay for it.
            # Falling back to the best free model keeps the task alive
            # instead of surfacing a provider error the operator has to
            # decode; when no free model remains, the result still says why
            # and the orchestrator's injection rescue takes over.
            retry = ""
            if not injected:
                if _looks_like_funds_failure(str(e)) and funds_tries < 2:
                    retry = _funds_retry(model, tried, available)
                    if retry:
                        funds_tries += 1
                        harness.progress(
                            "model %s rejected for billing/quota; retrying on free model %s"
                            % (model or "default", retry))
                if not retry:
                    retry = _retry_pick(model, tried, prefer_free, req.effort, str(e))
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
                              prefer_free, tried, agent_face=agent_face,
                              effort=req.effort)
        except subprocess.TimeoutExpired:
            harness.emit(False, "opencode timed out", 124)
            return
        except FileNotFoundError:
            harness.emit(False, "opencode binary not found", 127)
            return
        if not injected and not out["ok"]:
            retry = ""
            if _looks_like_funds_failure(out["result"]) and funds_tries < 2:
                retry = _funds_retry(model, tried, available)
                if retry:
                    funds_tries += 1
                    harness.progress(
                        "model %s rejected for billing/quota; retrying on free model %s"
                        % (model or "default", retry))
            if not retry and _looks_like_model_failure(out["result"]):
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
               prefer_free=False, tried=None, agent_face="", effort="",
               funds_tries=0):
    """One-shot plain text mode for builds without --format json."""
    # An injected run's model lives on panda's provider, not on this build's
    # roster: the rotate-and-retry below would swap in a free model and drop
    # the injection, so it is skipped entirely. A pinned model that this build
    # still serves is not that case, and keeps the retry.
    injected = _injected_provider
    if not model:
        # The user's ordered preference list still applies on this path.
        preferred, pref_variants, pref_notes, available = _preferred_fresh(
            _served_models())
        if preferred:
            for pref_note in pref_notes:
                harness.progress(pref_note)
            if effort and "#" not in preferred and effort in pref_variants:
                preferred += "#" + effort
            harness.progress("using preferred model %s" % preferred)
            model = preferred
        else:
            for pref_note in pref_notes:
                harness.progress(pref_note)
            # The built-in default rots as the free roster drifts; re-anchor
            # it against the live list too. When nothing matches, pick from the
            # served set with the same free-first policy the event path uses
            # instead of taking an arbitrary first entry.
            resolved, note, err = _resolve_model(DEFAULT_MODEL)
            if resolved:
                model = resolved
            else:
                available = _served_models()
                pick, variants = _pick_model(available, prefer_free)
                if pick:
                    if effort and effort in variants:
                        pick += "#" + effort
                    model = pick
                    note = f"model {DEFAULT_MODEL} is not available; using {model}"
                elif available:
                    model = available[0]
                    note = f"model {DEFAULT_MODEL} is not available; using {model}"
            if note:
                harness.progress(note)
    plain = ["opencode", "run", "--print-logs=false"]
    if injected and _cli_flags()["standalone"]:
        plain.append("--standalone")
    plain += auto + ["--model", model]
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
    if not injected and returncode != 0:
        detail = "%s\n%s" % (err, text)
        tried = tried if tried is not None else set()
        if _looks_like_funds_failure(detail) and funds_tries < 2:
            pick = _funds_retry(model, tried, _served_models())
            if pick:
                harness.progress("model %s rejected for billing/quota; "
                                 "retrying on free model %s" % (model, pick))
                return _run_plain(prompt, cwd, timeout, resume, pick, auto,
                                  prefer_free, tried, agent_face=agent_face,
                                  effort=effort, funds_tries=funds_tries + 1)
        if _looks_like_model_failure(detail):
            # Same ordered retry as the stream path: the preference list walks
            # on to its next entry, and only an exhausted list falls back to
            # re-probing the roster.
            if model:
                tried.add(model.split("#")[0])
            if len(tried) < 4:
                pick = _retry_pick(model, tried, prefer_free, effort, detail)
                if pick:
                    return _run_plain(prompt, cwd, timeout, resume, pick, auto,
                                      prefer_free, tried, agent_face=agent_face,
                                      effort=effort)
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
