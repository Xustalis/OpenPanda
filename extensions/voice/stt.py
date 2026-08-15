#!/usr/bin/env python3
"""Voice sidecar: speech-to-text (ASR).

Captures one utterance from the microphone and transcribes it. Backend selected
by PANDA_ASR_BACKEND: "faster-whisper" (default, local) or "google" (cloud).
Missing drivers/keys emit ok=false so the Go core degrades gracefully.

  stdin:  (unused; empty JSON optional)
  stdout: JSON {"ok": bool, "result": str, "exit_code": int}
          result is the transcript on success.

Local: pip install faster-whisper SpeechRecognition pyaudio
       (a Whisper model is downloaded on first run; PANDA_WHISPER_MODEL, default
        "base", selects the size — "tiny"/"base" fit the Orange Pi).
Cloud: pip install SpeechRecognition pyaudio (Google's free endpoint).
"""
import json
import os
import sys

BACKEND = os.environ.get("PANDA_ASR_BACKEND", "faster-whisper")
WHISPER_MODEL = os.environ.get("PANDA_WHISPER_MODEL", "base")


def main():
    try:
        import speech_recognition as sr
    except ImportError:
        _emit(False, "SpeechRecognition not installed (pip install SpeechRecognition pyaudio)", 3)
        return

    recognizer = sr.Recognizer()
    try:
        with sr.Microphone() as source:
            recognizer.adjust_for_ambient_noise(source, duration=0.5)
            audio = recognizer.listen(source)
    except Exception as exc:  # noqa: BLE001 — no mic / capture failure
        _emit(False, "microphone capture failed: %s" % exc, 4)
        return

    try:
        if BACKEND == "google":
            transcript = recognizer.recognize_google(audio)
        elif BACKEND == "faster-whisper":
            transcript = _faster_whisper(audio)
        else:
            _emit(False, "unknown PANDA_ASR_BACKEND %r" % BACKEND, 2)
            return
    except sr.UnknownValueError:
        _emit(False, "speech not understood", 1)
        return
    except sr.RequestError as exc:
        _emit(False, "asr service error: %s" % exc, 2)
        return
    except Exception as exc:  # noqa: BLE001 — missing faster-whisper / model
        _emit(False, "asr failed: %s" % exc, 2)
        return

    _emit(True, transcript.strip(), 0)


def _faster_whisper(audio):
    import numpy as np
    from faster_whisper import WhisperModel

    model = WhisperModel(WHISPER_MODEL, device="cpu", compute_type="int8")
    pcm = np.frombuffer(audio.get_raw_data(), np.int16).astype(np.float32) / 32768.0
    segments, _ = model.transcribe(pcm)
    return "".join(s.text for s in segments)


def _emit(ok, result, exit_code):
    print(json.dumps({"ok": bool(ok), "result": result, "exit_code": exit_code}, ensure_ascii=False))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
