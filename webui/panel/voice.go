// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// Speech-to-text for the composer's dictation button.
//
// The microphone belongs to the *browser*, not to this process. The console is
// routinely opened from another machine (`panda web` bound to a LAN address),
// and a server-side capture would then record the wrong room — so the frontend
// records and this handler only relays the clip to a transcription endpoint.
//
// That endpoint is OpenAI-compatible (`POST {base_url}/audio/transcriptions`,
// multipart `file` + `model`), which is the shape every vendor that serves
// Whisper already speaks, including the relays people put in front of them.
// The asr section is optional: an empty one inherits the active model's base
// URL and key, because the relay that answers chat usually answers Whisper too.
//
// Two things this deliberately does NOT do:
//
//   - It does not fall back to the local voice sidecars (extensions/voice).
//     Those open the microphone on the server, which is the wrong machine, and
//     they need faster-whisper installed to read a clip at all.
//   - It does not report "not configured" for an endpoint that simply refused.
//     The vendor's own message ("model whisper-1 does not exist") is the thing
//     the user can act on; a guess about missing config is not.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// asrTimeout bounds one transcription round-trip. A minute of speech is a few
// hundred KB and vendors answer in seconds; anything near this ceiling is a
// hung connection, and the button must come back rather than spin forever.
const asrTimeout = 90 * time.Second

// asrMaxClip bounds the clip this handler will forward. The middleware already
// caps a mutating request at maxAPIBodyBytes; this is the tighter, semantic
// bound — a dictation clip longer than this is a stuck microphone, not speech.
const asrMaxClip = 16 << 20

// asrExtByMime is the filename extension handed to the vendor. Whisper reads
// the container from the file name, so a browser blob with no name at all
// (`audio/webm` from MediaRecorder) has to be given one or every upload is
// rejected as an unknown format.
var asrExtByMime = map[string]string{
	"audio/webm":  "webm",
	"audio/ogg":   "ogg",
	"audio/mp4":   "mp4",
	"audio/mpeg":  "mp3",
	"audio/wav":   "wav",
	"audio/x-wav": "wav",
	"audio/wave":  "wav",
	"audio/flac":  "flac",
}

// transcribeAudio serves POST /api/voice/transcribe — one recorded clip in,
// one transcript out.
func (h *handler) transcribeAudio(w http.ResponseWriter, r *http.Request) {
	clip, name, err := readClip(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	target := h.asrTarget()
	if target.BaseURL == "" {
		// Nothing to call and nothing to inherit: this is the one genuine
		// configuration failure, and it names the key that fixes it.
		writeErr(w, http.StatusServiceUnavailable,
			errors.New("speech recognition has no endpoint: set asr.base_url (or model.base_url) in config.yaml"))
		return
	}

	text, err := transcribe(r.Context(), target, name, clip)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{"text": text})
}

// asrKeyEnv overrides the stored key, the same escape hatch the panel token
// and the shared secret have: a deployment that must not put a secret in
// config.yaml exports it instead.
const asrKeyEnv = "OPENPANDA_ASR_KEY"

// asrTarget resolves the configured section against the active model, so an
// empty asr section still yields a usable endpoint.
func (h *handler) asrTarget() config.ASRConfig {
	active := h.engineModel()
	var asr config.ASRConfig
	h.readCfg(func(c *config.Config) { asr = c.ASR })
	target := asr.ASRTarget(active)
	if key := strings.TrimSpace(os.Getenv(asrKeyEnv)); key != "" {
		target.APIKey = key
	}
	return target
}

// readClip streams the uploaded recording out of the multipart body. It reads
// one field, so a client that appends anything else is ignored rather than
// rejected — the browser only ever sends `audio`.
func readClip(r *http.Request) ([]byte, string, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", errors.New("multipart body required")
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, "", errors.New("no audio in the request")
		}
		if err != nil {
			return nil, "", errors.New("bad multipart body")
		}
		if part.FormName() != "audio" {
			_ = part.Close()
			continue
		}
		// Read the part's identity before the body: Close releases the headers.
		name := clipName(part.Header.Get("Content-Type"), part.FileName())
		buf, err := io.ReadAll(io.LimitReader(part, asrMaxClip+1))
		_ = part.Close()
		if err != nil {
			return nil, "", errors.New("could not read the recording")
		}
		if len(buf) == 0 {
			return nil, "", errors.New("the recording is empty")
		}
		if len(buf) > asrMaxClip {
			return nil, "", fmt.Errorf("the recording is larger than %d MiB", asrMaxClip>>20)
		}
		return buf, name, nil
	}
}

// clipName is the filename the vendor sees. MediaRecorder sends no name, and a
// vendor that infers the container from the extension then refuses the clip —
// so a missing (or extension-less) name is rebuilt from the MIME type.
func clipName(contentType, fileName string) string {
	if i := strings.LastIndex(fileName, "."); i > 0 {
		return fileName
	}
	mime := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	if ext, ok := asrExtByMime[strings.ToLower(mime)]; ok {
		return "dictation." + ext
	}
	// An unknown container still gets forwarded: the vendor's complaint about
	// the format is more useful than ours.
	return "dictation.webm"
}

// transcribe posts one clip to an OpenAI-compatible transcription endpoint.
// The upstream's own error body is what comes back to the console — it is the
// only text that says *why* ("model not found", "unsupported audio format"),
// and the key is never part of it.
func transcribe(ctx context.Context, target config.ASRConfig, name string, clip []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	// The file part is written by hand: CreateFormFile hard-codes
	// application/octet-stream, and some vendors sniff the part's own type.
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	hdr.Set("Content-Type", mimeForName(name))
	part, err := mw.CreatePart(hdr)
	if err != nil {
		return "", errors.New("could not build the upload")
	}
	if _, err := part.Write(clip); err != nil {
		return "", errors.New("could not build the upload")
	}
	if err := mw.WriteField("model", target.Model); err != nil {
		return "", errors.New("could not build the upload")
	}
	// A language hint is opt-in only. Guessing it from the console's locale
	// would transcribe a Chinese sentence as English whenever the UI happens to
	// be in English; vendors detect the language themselves when not told.
	if target.Language != "" {
		_ = mw.WriteField("language", target.Language)
	}
	_ = mw.WriteField("response_format", "json")
	if err := mw.Close(); err != nil {
		return "", errors.New("could not build the upload")
	}

	ctx, cancel := context.WithTimeout(ctx, asrTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		target.BaseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", errors.New("speech recognition endpoint is not a valid URL")
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if target.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+target.APIKey)
	}

	resp, err := (&http.Client{Timeout: asrTimeout}).Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("speech recognition timed out")
		}
		return "", fmt.Errorf("speech recognition is unreachable: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("speech recognition failed (%d): %s", resp.StatusCode, upstreamReason(raw))
	}
	// The documented shape is {"text": "..."}; some gateways answer with the
	// bare string, and a few wrap it one level deeper. All three are read, and
	// a body that is none of them is reported as text rather than swallowed.
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err == nil && strings.TrimSpace(out.Text) != "" {
		return strings.TrimSpace(out.Text), nil
	}
	var nested struct {
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &nested); err == nil && strings.TrimSpace(nested.Data.Text) != "" {
		return strings.TrimSpace(nested.Data.Text), nil
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil && strings.TrimSpace(plain) != "" {
		return strings.TrimSpace(plain), nil
	}
	return "", fmt.Errorf("speech recognition returned no text: %s", upstreamReason(raw))
}

// upstreamReason digs the human sentence out of an error body. Vendors answer
// with {"error":{"message":…}}, {"message":…}, {"error":"…"} or plain text; the
// raw JSON shown to the user is the one outcome that helps nobody.
func upstreamReason(raw []byte) string {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if len(envelope.Error) > 0 {
			var asText string
			if err := json.Unmarshal(envelope.Error, &asText); err == nil && asText != "" {
				return clipReason(asText)
			}
			var asObj struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(envelope.Error, &asObj); err == nil && asObj.Message != "" {
				return clipReason(asObj.Message)
			}
		}
		if envelope.Message != "" {
			return clipReason(envelope.Message)
		}
		if envelope.Detail != "" {
			return clipReason(envelope.Detail)
		}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "no reason given"
	}
	return clipReason(string(raw))
}

// clipReason keeps an upstream message to one readable line: these end up in a
// toast, and an HTML error page pasted into a toast is worse than a summary.
func clipReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// mimeForName maps the outgoing filename back to a content type.
func mimeForName(name string) string {
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return "application/octet-stream"
	}
	switch strings.ToLower(name[dot+1:]) {
	case "webm":
		return "audio/webm"
	case "ogg":
		return "audio/ogg"
	case "mp4", "m4a":
		return "audio/mp4"
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "flac":
		return "audio/flac"
	}
	return "application/octet-stream"
}
