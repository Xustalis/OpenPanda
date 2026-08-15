#!/usr/bin/env python3
"""Voice sidecar: text-to-speech (TTS).

Speaks the text from stdin. Backend selected by PANDA_TTS_BACKEND: "piper"
(default, local neural) or "pyttsx3" (espeak fallback).

  stdin:  JSON {"text": str}
  stdout: JSON {"ok": bool, "result": str, "exit_code": int}

Local:    pip install piper-tts sounddevice  (plus a .onnx voice at PANDA_PIPER_MODEL)
Fallback: pip install pyttsx3  (plus a platform backend, e.g. espeak)
"""
import json
import os
import sys

BACKEND = os.environ.get("PANDA_TTS_BACKEND", "piper")


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
        if BACKEND == "pyttsx3":
            _speak_pyttsx3(text)
        elif BACKEND == "piper":
            _speak_piper(text)
        else:
            _emit(False, "unknown PANDA_TTS_BACKEND %r" % BACKEND, 2)
            return
    except Exception as exc:  # noqa: BLE001 — driver/model may be absent
        _emit(False, "tts failed: %s" % exc, 4)
        return

    _emit(True, "spoken", 0)


def _speak_pyttsx3(text):
    import pyttsx3
    engine = pyttsx3.init()
    engine.say(text)
    engine.runAndWait()


def _speak_piper(text):
    from piper import PiperVoice
    import numpy as np
    import sounddevice as sd

    model = os.environ.get("PANDA_PIPER_MODEL", "")
    if not model:
        raise RuntimeError("PANDA_PIPER_MODEL not set")
    voice = PiperVoice.load(model)
    chunks = []
    for chunk in voice.synthesize_stream_raw(text):
        chunks.append(chunk)
    audio = np.concatenate(chunks)
    sd.play(audio, voice.config.sample_rate)
    sd.wait()


def _emit(ok, result, exit_code):
    print(json.dumps({"ok": bool(ok), "result": result, "exit_code": exit_code}, ensure_ascii=False))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
