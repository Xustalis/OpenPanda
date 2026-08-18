#!/usr/bin/env python3
"""Voice sidecar: wake-word detection ("hey panda").

Backend selected by OPENPANDA_WAKE_BACKEND: "openwakeword" (default, open source,
no key) or "pvporcupine" (needs a Picovoice access key in OPENPANDA_PORCUPINE_KEY).

  stdin:  JSON {"duration_s": float}  (optional; seconds to listen, 0 = forever)
  stdout: JSON {"ok": bool, "result": str, "exit_code": int}
          result is "wake" on detection, "timeout" otherwise.

Local:  pip install openwakeword pyaudio numpy

Wake word (P2-21): neither backend's built-in keyword table contains
"hey_panda", so pointing the default at it made the extension fail out of the
box. The defaults below are built-in keywords that actually ship with each
backend; override with OPENPANDA_WAKE_KEYWORD (built-in name) or, for
openwakeword, OPENPANDA_WAKE_MODEL (path to a custom .tflite model, e.g. a trained
"hey panda").
"""
import json
import os
import sys
import time

BACKEND = os.environ.get("OPENPANDA_WAKE_BACKEND", "openwakeword")
DEFAULT_DURATION = 10.0

# Built-in keywords guaranteed present in each backend's default model set.
DEFAULT_KEYWORD_OWW = "hey_jarvis"
DEFAULT_KEYWORD_PORCUPINE = "porcupine"


def main():
    raw = sys.stdin.read()
    try:
        req = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        _emit(False, "invalid request JSON", 2)
        return

    duration = float(req.get("duration_s", DEFAULT_DURATION))

    try:
        if BACKEND == "pvporcupine":
            detected = _porcupine(duration)
        elif BACKEND == "openwakeword":
            detected = _openwakeword(duration)
        else:
            _emit(False, "unknown OPENPANDA_WAKE_BACKEND %r" % BACKEND, 2)
            return
    except Exception as exc:  # noqa: BLE001 — missing driver / mic / model
        _emit(False, "wake failed: %s" % exc, 4)
        return

    _emit(detected, "wake" if detected else "timeout", 0)


def _openwakeword(duration):
    import numpy as np
    import pyaudio
    from openwakeword.model import Model

    model_path = os.environ.get("OPENPANDA_WAKE_MODEL", "")
    keyword = os.environ.get("OPENPANDA_WAKE_KEYWORD", "") or DEFAULT_KEYWORD_OWW
    oww = Model(wakeword_models=[model_path] if model_path else [keyword])

    pa = pyaudio.PyAudio()
    stream = pa.open(
        rate=16000, channels=1, format=pyaudio.paInt16,
        input=True, frames_per_buffer=1280,
    )
    deadline = time.time() + duration if duration > 0 else None
    detected = False
    try:
        while deadline is None or time.time() < deadline:
            pcm = stream.read(1280, exception_on_overflow=False)
            frame = np.frombuffer(pcm, np.int16)
            scores = oww.predict(frame)
            if any(v > 0.5 for v in scores.values()):
                detected = True
                break
    finally:
        stream.close()
        pa.terminate()
    return detected


def _porcupine(duration):
    key = os.environ.get("OPENPANDA_PORCUPINE_KEY", "")
    if not key:
        raise RuntimeError("OPENPANDA_PORCUPINE_KEY not set")
    import pvporcupine
    import pyaudio

    keyword = os.environ.get("OPENPANDA_WAKE_KEYWORD", "") or DEFAULT_KEYWORD_PORCUPINE
    porcupine = pvporcupine.create(access_key=key, keywords=[keyword])
    pa = pyaudio.PyAudio()
    stream = pa.open(
        rate=porcupine.sample_rate, channels=1, format=pyaudio.paInt16,
        input=True, frames_per_buffer=porcupine.frame_length,
    )
    deadline = time.time() + duration if duration > 0 else None
    detected = False
    try:
        while deadline is None or time.time() < deadline:
            pcm = stream.read(porcupine.frame_length, exception_on_overflow=False)
            if porcupine.process(pcm) >= 0:
                detected = True
                break
    finally:
        stream.close()
        pa.terminate()
        porcupine.delete()
    return detected


def _emit(ok, result, exit_code):
    print(json.dumps({"ok": bool(ok), "result": result, "exit_code": exit_code}, ensure_ascii=False))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
