package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/mdtext"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
)

// voiceDir is where the voice sidecars live, resolved relative to the working
// directory. It is a var so tests can point it at a temp dir.
var voiceDir = "extensions/voice"

// Transcript is one captured utterance: the wake word fired and the speech that
// followed was transcribed. OK is false (with Err set) when a sidecar is
// unavailable — a missing key or driver degrades, it never crashes the caller.
//
// Timeout separates the two very different reasons OK can be false. Nobody spoke
// is the normal outcome of a listening round and the caller should simply go
// around again; a broken sidecar will break identically on the next round, so a
// caller that cannot tell them apart either spins a hot loop respawning python
// or reports a missing driver as silence.
type Transcript struct {
	OK      bool
	Text    string
	Timeout bool
	Err     string
}

// Listen blocks until the wake word fires (or durationS elapses), then captures
// and transcribes the utterance. It drives wake.py then stt.py. VAD is
// deliberately NOT in this chain yet: each sidecar opens its own microphone
// stream, so a vad.py gate would consume the start of the utterance and leave
// stt.py with silence. VAD belongs in a single-stream capture (wake + VAD + ASR
// sharing one audio feed), which is a hardware-phase follow-up.
func Listen(ctx context.Context, durationS float64) Transcript {
	wake := runSidecar(ctx, "wake.py", map[string]any{"duration_s": durationS})
	if !wake.ok {
		return Transcript{OK: false, Err: wake.reason()}
	}
	if wake.result != "wake" {
		return Transcript{OK: false, Timeout: true, Err: wake.result} // "timeout"
	}
	stt := runSidecar(ctx, "stt.py", nil)
	if !stt.ok {
		return Transcript{OK: false, Err: stt.reason()}
	}
	return Transcript{OK: true, Text: stt.result}
}

// Speak speaks text via the TTS sidecar. A missing driver is returned as an
// error, not fatal. The text is stripped of Markdown first — emphasis
// markers, fences and table pipes read as noise ("星号星号", "反引号") when
// spoken, so the pipeline always renders answers to prose before TTS.
func Speak(ctx context.Context, text string) error {
	res := runSidecar(ctx, "tts.py", map[string]any{"text": mdtext.Plain(text)})
	if !res.ok {
		return fmt.Errorf("tts: %s", res.reason())
	}
	return nil
}

// sidecarResult is the parsed stdout of a voice sidecar.
type sidecarResult struct {
	ok     bool
	result string
	err    string
}

// reason is the failure message of a non-ok result. The two fields carry it in
// two different situations and a caller that reads only one loses the message:
// err holds a spawn/exit/protocol failure (the sidecar never answered), while a
// sidecar that ran fine and *reported* a failure ("No module named 'numpy'")
// puts its reason in result.
func (r sidecarResult) reason() string {
	if r.err != "" {
		return r.err
	}
	if r.result != "" {
		return r.result
	}
	return "sidecar failed with no reason given"
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
	cmd, ok := pyexec.Command(ctx, voiceDir+"/"+name)
	if !ok {
		return sidecarResult{ok: false, err: "no Python 3 interpreter found for the voice sidecar" +
			" — install Python 3 or set " + pyexec.EnvOverride}
	}
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr executil.Capture
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
