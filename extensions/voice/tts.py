#!/usr/bin/env python3
"""Voice sidecar: text-to-speech (TTS).

Speaks the text from stdin and emits ok=true when finished. Uses pyttsx3
(local, offline) by default.

  stdin:  JSON {"text": str}
  stdout: JSON {"ok": bool, "result": str, "exit_code": int}

Requires `pip install pyttsx3` (and a platform TTS backend, e.g. espeak/nsss).
Missing driver emits ok=false.
"""
import json
import sys


def main():
    raw = sys.stdin.read()
    try:
        req = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        _emit(False, "invalid request JSON", 2)
        return

    text = req.get("text", "").strip()
    if not text:
        _emit(False, "no text to speak", 2)
        return

    try:
        import pyttsx3
    except ImportError:
        _emit(False, "pyttsx3 not installed (pip install pyttsx3)", 3)
        return

    try:
        engine = pyttsx3.init()
        engine.say(text)
        engine.runAndWait()
    except Exception as exc:  # noqa: BLE001 — audio backend may be absent
        _emit(False, "tts failed: %s" % exc, 4)
        return

    _emit(True, "spoken", 0)


def _emit(ok, result, exit_code):
    print(json.dumps({"ok": bool(ok), "result": result, "exit_code": exit_code}, ensure_ascii=False))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
