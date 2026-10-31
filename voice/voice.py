"""Local audio helper for Hanoi. JSON lines on stdin/stdout; audio stays on this machine."""

import argparse
import json
import queue
import subprocess
import sys
import tempfile
import threading
import wave
from pathlib import Path

import numpy as np
import sounddevice as sd
import soundfile as sf


def emit(kind, **fields):
    print(json.dumps({"type": kind, **fields}, ensure_ascii=False), flush=True)


class Voice:
    def __init__(self, whisper_model, piper_model):
        self.whisper_model = whisper_model
        self.piper_model = piper_model
        self.stream = None
        self.frames = []
        self.lock = threading.Lock()
        self.speaking = threading.Lock()
        self.generation = 0

    def start(self):
        sd.stop()
        with self.lock:
            if self.stream is not None:
                return
            self.generation += 1
            self.frames = []

            def callback(indata, frames, time_info, status):
                with self.lock:
                    self.frames.append(indata.copy())

            self.stream = sd.InputStream(samplerate=16000, channels=1, dtype="int16", callback=callback)
            try:
                self.stream.start()
            except Exception:
                self.stream.close()
                self.stream = None
                raise
        emit("recording")

    def stop(self):
        with self.lock:
            stream = self.stream
            self.stream = None
        if stream is None:
            return
        stream.stop()
        stream.close()
        with self.lock:
            frames = self.frames
            self.frames = []
        emit("transcribing")
        threading.Thread(target=self.transcribe, args=(frames,), daemon=True).start()

    def transcribe(self, frames):
        try:
            if not frames:
                emit("transcript", text="")
                return
            with tempfile.TemporaryDirectory() as directory:
                audio = Path(directory) / "input.wav"
                output = Path(directory) / "result"
                with wave.open(str(audio), "wb") as wav:
                    wav.setnchannels(1)
                    wav.setsampwidth(2)
                    wav.setframerate(16000)
                    wav.writeframes(np.concatenate(frames).tobytes())
                subprocess.run(
                    ["whisper-cli", "-m", self.whisper_model, "-f", str(audio), "-l", "de", "-nt", "-otxt", "-of", str(output)],
                    check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180,
                )
                emit("transcript", text=Path(str(output) + ".txt").read_text(encoding="utf-8").strip())
        except Exception as exc:
            emit("error", error=f"transcription: {exc}")

    def speak(self, text):
        if not text.strip():
            return
        generation = self.generation
        # A new recording stops playback. Speech synthesis runs outside the input loop.
        def run():
            try:
                with self.speaking, tempfile.TemporaryDirectory() as directory:
                    audio = Path(directory) / "speech.wav"
                    subprocess.run(
                        ["piper", "--model", self.piper_model, "--output_file", str(audio)],
                        input=text, text=True, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180,
                    )
                    samples, rate = sf.read(str(audio), dtype="float32")
                    with self.lock:
                        current = generation == self.generation and self.stream is None
                    if current:
                        sd.play(samples, rate)
            except Exception as exc:
                emit("error", error=f"speech: {exc}")

        threading.Thread(target=run, daemon=True).start()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--whisper-model", required=True)
    parser.add_argument("--piper-model", required=True)
    args = parser.parse_args()
    voice = Voice(args.whisper_model, args.piper_model)
    requests = queue.Queue()

    def read_input():
        for line in sys.stdin:
            try:
                requests.put(json.loads(line))
            except json.JSONDecodeError as exc:
                emit("error", error=str(exc))
        requests.put(None)

    threading.Thread(target=read_input, daemon=True).start()
    while (request := requests.get()) is not None:
        try:
            kind = request.get("type")
            if kind == "start":
                voice.start()
            elif kind == "stop":
                voice.stop()
            elif kind == "speak":
                voice.speak(request.get("text", ""))
        except Exception as exc:
            emit("error", error=str(exc))
    if voice.stream is not None:
        voice.stream.stop()
        voice.stream.close()
    sd.stop()


if __name__ == "__main__":
    main()
