#!/usr/bin/env python3
"""Voice sidecar: speech-to-text (ASR).

Captures one utterance from the microphone and transcribes it. Backend selected
by PANDA_ASR_BACKEND ("google" default; "whisper" for local Whisper).

  stdin:  (unused; empty JSON optional)
  stdout: JSON {"ok": bool, "result": str, "exit_code": int}
          result is the transcript on success.

Requires `pip install SpeechRecognition pyaudio` (plus the chosen backend).
Missing drivers/keys emit ok=false so the Go core degrades gracefully.
"""
import json
import os
import sys


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

    backend = os.environ.get("PANDA_ASR_BACKEND", "google")
    try:
        if backend == "whisper":
            transcript = recognizer.recognize_whisper(audio)
        else:
            transcript = recognizer.recognize_google(audio)
    except sr.UnknownValueError:
        _emit(False, "speech not understood", 1)
        return
    except sr.RequestError as exc:
        _emit(False, "asr service error: %s" % exc, 2)
        return

    _emit(True, transcript, 0)


def _emit(ok, result, exit_code):
    print(json.dumps({"ok": bool(ok), "result": result, "exit_code": exit_code}, ensure_ascii=False))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
