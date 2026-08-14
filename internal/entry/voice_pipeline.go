package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// voiceDir is where the voice sidecars live, resolved relative to the working
// directory. It is a var so tests can point it at a temp dir.
var voiceDir = "extensions/voice"

// Transcript is one captured utterance: the wake word fired and the speech that
// followed was transcribed. OK is false (with Err set) when a sidecar is
// unavailable — a missing key or driver degrades, it never crashes the caller.
type Transcript struct {
	OK   bool
	Text string
	Err  string
}

// Listen blocks until the wake word fires (or durationS elapses), then captures
// and transcribes the utterance. It drives wake.py then stt.py. This is the
// voice entry into the unified entry model: the returned Text is fed to
// Classify exactly like a typed `panda ask` prompt.
func Listen(ctx context.Context, durationS float64) Transcript {
	wake := runSidecar(ctx, "wake.py", map[string]any{"duration_s": durationS})
	if !wake.ok {
		return Transcript{OK: false, Err: wake.err}
	}
	if wake.result != "wake" {
		return Transcript{OK: false, Err: wake.result} // "timeout"
	}
	stt := runSidecar(ctx, "stt.py", nil)
	return Transcript{OK: stt.ok, Text: stt.result, Err: stt.err}
}

// Speak speaks text via the TTS sidecar. A missing driver is returned as an
// error, not fatal.
func Speak(ctx context.Context, text string) error {
	res := runSidecar(ctx, "tts.py", map[string]any{"text": text})
	if !res.ok {
		if res.err != "" {
			return fmt.Errorf("tts: %s", res.err)
		}
		return fmt.Errorf("tts: %s", res.result)
	}
	return nil
}

// sidecarResult is the parsed stdout of a voice sidecar.
type sidecarResult struct {
	ok     bool
	result string
	err    string
}

// runSidecar spawns extensions/voice/<name> with a JSON request on stdin and
// parses {ok, result} from stdout — the same protocol as the agent adapters.
// stderr is only inspected on a non-zero exit (error reporting), never merged
// into a successful result.
func runSidecar(ctx context.Context, name string, req map[string]any) sidecarResult {
	var in []byte
	if req != nil {
		in, _ = json.Marshal(req)
	}
	cmd := exec.CommandContext(ctx, "python3", voiceDir+"/"+name)
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if msg == "" {
			msg = err.Error()
		}
		return sidecarResult{ok: false, err: msg}
	}
	var out struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return sidecarResult{ok: false, err: "sidecar output not JSON: " + stdout.String()}
	}
	return sidecarResult{ok: out.OK, result: out.Result}
}
