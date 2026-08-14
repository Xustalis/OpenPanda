#!/usr/bin/env python3
"""Voice sidecar: Porcupine wake-word detection ("hey panda").

Protocol (shared with the other voice sidecars):
  stdin:  JSON {"duration_s": <float>}  (optional; seconds to listen, 0 = forever)
  stdout: JSON {"ok": bool, "result": str, "exit_code": int}
          result is "wake" on detection, "timeout" otherwise.

Requires `pip install pvporcupine pyaudio` and a Picovoice access key in
PANDA_PORCUPINE_KEY. Without them this emits ok=false with a clear message, so
the Go core can degrade (voice disabled) instead of crashing.
"""
import json
import os
import sys
import time

DEFAULT_DURATION = 10.0
KEYWORD = "hey panda"


def main():
    raw = sys.stdin.read()
    try:
        req = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        _emit(False, "invalid request JSON", 2)
        return

    key = os.environ.get("PANDA_PORCUPINE_KEY", "")
    duration = float(req.get("duration_s", DEFAULT_DURATION))

    if not key:
        _emit(False, "PANDA_PORCUPINE_KEY not set", 3)
        return

    try:
        import pvporcupine
    except ImportError:
        _emit(False, "pvporcupine not installed (pip install pvporcupine pyaudio)", 3)
        return

    try:
        porcupine = pvporcupine.create(
            access_key=key,
            keywords=[KEYWORD.replace(" ", "_")],
        )
    except Exception as exc:  # noqa: BLE001 — surface any init failure to Go
        _emit(False, "porcupine init failed: %s" % exc, 4)
        return

    import pyaudio
    pa = pyaudio.PyAudio()
    stream = pa.open(
        rate=porcupine.sample_rate,
        channels=1,
        format=pyaudio.paInt16,
        input=True,
        frames_per_buffer=porcupine.frame_length,
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

    _emit(detected, "wake" if detected else "timeout", 0)


def _emit(ok, result, exit_code):
    print(json.dumps({"ok": bool(ok), "result": result, "exit_code": exit_code}, ensure_ascii=False))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
