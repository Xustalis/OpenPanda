#!/usr/bin/env python3
"""Voice sidecar: voice activity detection (VAD).

Listens for duration_s and classifies the window as speech or silence, used to
segment a spoken utterance before ASR.

  stdin:  JSON {"duration_s": float}  (default 5)
  stdout: JSON {"ok": bool, "result": str, "exit_code": int}
          result is "speech" or "silence".

Requires `pip install webrtcvad pyaudio`. Missing driver emits ok=false.
"""
import json
import sys
import time


def main():
    raw = sys.stdin.read()
    try:
        req = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        _emit(False, "invalid request JSON", 2)
        return

    duration = float(req.get("duration_s", 5.0))

    try:
        import webrtcvad
    except ImportError:
        _emit(False, "webrtcvad not installed (pip install webrtcvad pyaudio)", 3)
        return

    import pyaudio
    vad = webrtcvad.Vad(2)  # aggressiveness 0-3
    pa = pyaudio.PyAudio()
    stream = pa.open(
        format=pyaudio.paInt16, channels=1, rate=16000,
        input=True, frames_per_buffer=320,
    )

    speech_frames = 0
    total_frames = 0
    deadline = time.time() + duration
    try:
        while time.time() < deadline:
            frame = stream.read(320, exception_on_overflow=False)
            total_frames += 1
            if vad.is_speech(frame, 16000):
                speech_frames += 1
    finally:
        stream.close()
        pa.terminate()

    ratio = speech_frames / total_frames if total_frames else 0.0
    _emit(True, "speech" if ratio > 0.3 else "silence", 0)


def _emit(ok, result, exit_code):
    print(json.dumps({"ok": bool(ok), "result": result, "exit_code": exit_code}, ensure_ascii=False))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
