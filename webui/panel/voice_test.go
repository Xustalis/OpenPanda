// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// voicePanel wires a panel whose config points speech-to-text at whatever the
// caller supplies. No engine: the handler only reads config, and a real engine
// would need a model endpoint nothing listens on.
func voicePanel(t *testing.T, mutate func(*config.Config)) http.Handler {
	t.Helper()
	cfg := config.Default()
	mutate(cfg)
	return New(Deps{
		Store:     newTestStore(t),
		Cfg:       cfg,
		StaticDir: t.TempDir(),
		Token:     testToken,
	})
}

// clipRequest builds the multipart body the browser sends: one `audio` part
// with a MIME type and no filename, which is exactly what MediaRecorder's blob
// produces.
func clipRequest(t *testing.T, target, contentType string, clip []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="audio"`)
	if contentType != "" {
		hdr.Set("Content-Type", contentType)
	}
	part, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(clip); err != nil {
		t.Fatalf("write clip: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := authedReq(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// TestTranscribeRelaysToVendor pins the wire contract: the clip reaches
// {base_url}/audio/transcriptions as a named file part, the configured model
// rides along, and the transcript comes back as {"text":…}.
func TestTranscribeRelaysToVendor(t *testing.T) {
	var gotPath, gotModel, gotFile, gotType, gotAuth string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("upstream parse: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		gotModel = r.FormValue("model")
		file, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("upstream file: %v", err)
			return
		}
		defer file.Close()
		gotFile = hdr.Filename
		gotType = hdr.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(file)
		_, _ = w.Write([]byte(`{"text":"  你好，熊猫  "}`))
	}))
	defer upstream.Close()

	h := voicePanel(t, func(c *config.Config) {
		c.ASR = config.ASRConfig{BaseURL: upstream.URL + "/", APIKey: "asr-key", Model: "whisper-1"}
	})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, clipRequest(t, "/api/voice/transcribe", "audio/webm;codecs=opus", []byte("OPUSCLIP")))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}

	var out struct{ Text string }
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The transcript is trimmed: a leading space would land in the composer.
	if out.Text != "你好，熊猫" {
		t.Fatalf("text = %q", out.Text)
	}
	if gotPath != "/audio/transcriptions" {
		t.Errorf("upstream path = %q", gotPath)
	}
	if gotModel != "whisper-1" {
		t.Errorf("model = %q", gotModel)
	}
	// A blob with no filename must still arrive named, or the vendor refuses it
	// as an unknown container.
	if gotFile != "dictation.webm" {
		t.Errorf("filename = %q, want dictation.webm", gotFile)
	}
	if gotType != "audio/webm" {
		t.Errorf("part content type = %q", gotType)
	}
	if gotAuth != "Bearer asr-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if string(gotBody) != "OPUSCLIP" {
		t.Errorf("clip body = %q", gotBody)
	}
}

// TestTranscribeInheritsModelEndpoint covers the zero-config case: no asr
// section at all, so the active model's endpoint and key are borrowed and the
// default Whisper model is asked for.
func TestTranscribeInheritsModelEndpoint(t *testing.T) {
	var gotModel, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotModel = r.FormValue("model")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer upstream.Close()

	h := voicePanel(t, func(c *config.Config) {
		c.Model.BaseURL = upstream.URL
		c.Model.APIKey = "model-key"
	})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, clipRequest(t, "/api/voice/transcribe", "audio/webm", []byte("x")))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	if gotModel != config.DefaultASRModel {
		t.Errorf("model = %q, want %q", gotModel, config.DefaultASRModel)
	}
	if gotAuth != "Bearer model-key" {
		t.Errorf("auth = %q", gotAuth)
	}
}

// TestTranscribeNoEndpoint: with neither an asr section nor a model endpoint
// there is nothing to call, and the message has to name the key that fixes it.
func TestTranscribeNoEndpoint(t *testing.T) {
	h := voicePanel(t, func(c *config.Config) {
		c.Model.BaseURL = ""
		c.ASR = config.ASRConfig{}
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, clipRequest(t, "/api/voice/transcribe", "audio/webm", []byte("x")))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "asr.base_url") {
		t.Fatalf("503 body must name the key: %s", rr.Body.String())
	}
}

// TestTranscribeVendorErrorIsPassedThrough: the vendor's own sentence is the
// only text that says why, so it has to survive the round trip — while the
// API key must not.
func TestTranscribeVendorErrorIsPassedThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model whisper-1 does not exist","type":"invalid_request_error"}}`))
	}))
	defer upstream.Close()

	h := voicePanel(t, func(c *config.Config) {
		c.ASR = config.ASRConfig{BaseURL: upstream.URL, APIKey: "secret-key", Model: "whisper-1"}
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, clipRequest(t, "/api/voice/transcribe", "audio/webm", []byte("x")))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "model whisper-1 does not exist") {
		t.Fatalf("vendor reason lost: %s", body)
	}
	if strings.Contains(body, "secret-key") {
		t.Fatalf("the API key must never reach the console: %s", body)
	}
}

// TestTranscribeRejectsEmptyClip keeps a silent recording from becoming a
// vendor round-trip.
func TestTranscribeRejectsEmptyClip(t *testing.T) {
	h := voicePanel(t, func(c *config.Config) {
		c.ASR = config.ASRConfig{BaseURL: "http://127.0.0.1:9", Model: "whisper-1"}
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, clipRequest(t, "/api/voice/transcribe", "audio/webm", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestClipName(t *testing.T) {
	cases := []struct {
		ctype, name, want string
	}{
		{"audio/webm;codecs=opus", "", "dictation.webm"},
		{"audio/ogg", "", "dictation.ogg"},
		{"audio/mp4", "", "dictation.mp4"},
		{"audio/wav", "", "dictation.wav"},
		// An explicit filename wins — a vendor may key off it.
		{"audio/webm", "clip.webm", "clip.webm"},
		// Unknown container still gets a plausible extension rather than a
		// bare name the vendor would reject outright.
		{"application/octet-stream", "", "dictation.webm"},
	}
	for _, c := range cases {
		if got := clipName(c.ctype, c.name); got != c.want {
			t.Errorf("clipName(%q,%q) = %q, want %q", c.ctype, c.name, got, c.want)
		}
	}
}
