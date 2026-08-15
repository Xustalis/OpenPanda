#!/usr/bin/env python3
"""Voice sidecar: wake-word detection ("hey panda").

Backend selected by PANDA_WAKE_BACKEND: "openwakeword" (default, open source,
no key) or "pvporcupine" (needs a Picovoice access key in PANDA_PORCUPINE_KEY).

  stdin:  JSON {"duration_s": float}  (optional; seconds to listen, 0 = forever)
  stdout: JSON {"ok": bool, "result": str, "exit_code": int}
          result is "wake" on detection, "timeout" otherwise.

Local:  pip install openwakeword pyaudio numpy
        A bundled model is used by default (set PANDA_WAKE_MODEL to a .tflite
        path to override; a custom "hey panda" model is a follow-up).
"""
import json
import os
import sys
import time

BACKEND = os.environ.get("PANDA_WAKE_BACKEND", "openwakeword")
DEFAULT_DURATION = 10.0


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
            _emit(False, "unknown PANDA_WAKE_BACKEND %r" % BACKEND, 2)
            return
    except Exception as exc:  # noqa: BLE001 — missing driver / mic / model
        _emit(False, "wake failed: %s" % exc, 4)
        return

    _emit(detected, "wake" if detected else "timeout", 0)


def _openwakeword(duration):
    import numpy as np
    import pyaudio
    from openwakeword.model import Model

    model_path = os.environ.get("PANDA_WAKE_MODEL", "")
    oww = Model(wakeword_models=[model_path] if model_path else ["hey_jarvis"])

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
    key = os.environ.get("PANDA_PORCUPINE_KEY", "")
    if not key:
        raise RuntimeError("PANDA_PORCUPINE_KEY not set")
    import pvporcupine
    import pyaudio

    porcupine = pvporcupine.create(access_key=key, keywords=["hey_panda"])
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
