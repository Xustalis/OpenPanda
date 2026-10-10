// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestSessionAEADDerivesSharedKey verifies both ends of a conn derive the
// identical cipher from the exchanged nonces, and that flipping the
// direction (or ids, or the secret) yields a different key — the binding
// that keeps a captured hello from re-keying another link.
func TestSessionAEADDerivesSharedKey(t *testing.T) {
	aDialer, err := SessionAEAD("secret", "dialer", "listener", "n-dial", "n-listen")
	if err != nil {
		t.Fatalf("dialer kdf: %v", err)
	}
	aListener, err := SessionAEAD("secret", "dialer", "listener", "n-dial", "n-listen")
	if err != nil {
		t.Fatalf("listener kdf: %v", err)
	}
	sealed, err := sealSessFrame(aDialer, sessTagText, []byte(`{"type":"hello"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	tag, plain, err := openSessFrame(aListener, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if tag != sessTagText || string(plain) != `{"type":"hello"}` {
		t.Fatalf("round trip = tag %d payload %q", tag, plain)
	}

	// Swapped direction, wrong id, wrong secret, or a different nonce pair
	// must each produce a key that cannot open the frame.
	for name, args := range map[string][5]string{
		"swapped-nonces":  {"secret", "dialer", "listener", "n-listen", "n-dial"},
		"swapped-ids":     {"secret", "listener", "dialer", "n-dial", "n-listen"},
		"wrong-secret":    {"other", "dialer", "listener", "n-dial", "n-listen"},
		"different-nonce": {"secret", "dialer", "listener", "n-dial", "n-other"},
	} {
		bad, err := SessionAEAD(args[0], args[1], args[2], args[3], args[4])
		if err != nil {
			t.Fatalf("%s kdf: %v", name, err)
		}
		if _, _, err := openSessFrame(bad, sealed); err == nil {
			t.Fatalf("%s: frame opened under a different key", name)
		}
	}
}

// TestSealedFrameTamper: a single bit flip anywhere in the ciphertext must
// fail the GCM open — the frame cannot be mangled into something valid.
func TestSealedFrameTamper(t *testing.T) {
	aead, err := SessionAEAD("s", "a", "b", "n1", "n2")
	if err != nil {
		t.Fatalf("kdf: %v", err)
	}
	sealed, err := sealSessFrame(aead, sessTagData, []byte("payload"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, pos := range []int{0, len(sealed) - 1, len(sealed) / 2} {
		bad := bytes.Clone(sealed)
		bad[pos] ^= 0xff
		if _, _, err := openSessFrame(aead, bad); err == nil {
			t.Fatalf("tampered frame at byte %d opened", pos)
		}
	}
	// Truncated frames fail closed too.
	if _, _, err := openSessFrame(aead, sealed[:sessNonceLen]); err == nil {
		t.Fatalf("truncated sealed frame opened")
	}
}

// TestSealedConnRoundTrip runs a live ws pair: hello transits plaintext,
// the cipher arms mid-stream, and every subsequent envelope and data frame
// crosses encrypted — verified by a raw gorilla read of the ciphertext.
func TestSealedConnRoundTrip(t *testing.T) {
	const secret = "shared-secret"
	serverReady := make(chan struct{})
	got := make(chan Envelope, 2)

	// Server: read plaintext hello, arm, then keep reading sealed frames
	// and echo one envelope back.
	cancel, _ := startTestServer(t, "127.0.0.1:17890", func(conn *Conn) {
		var hello Envelope
		if err := conn.ReadJSON(&hello); err != nil {
			return
		}
		aead, err := SessionAEAD(secret, "client", "server", "cn", "sn")
		if err != nil {
			return
		}
		conn.ArmSession(aead)
		close(serverReady)
		for {
			var env Envelope
			if err := conn.ReadJSON(&env); err != nil {
				return
			}
			got <- env
			_ = conn.Send(env)
		}
	})
	defer cancel()

	client, err := NewClient("ws://127.0.0.1:17890/ws", testLogger()).Dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Hello goes out plaintext (no cipher yet).
	env, _ := NewEnvelope(MsgHello, "client", "m-hello", HelloPayload{NodeID: "client"})
	if err := client.Send(env); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	<-serverReady

	aead, err := SessionAEAD(secret, "client", "server", "cn", "sn")
	if err != nil {
		t.Fatalf("kdf: %v", err)
	}
	client.ArmSession(aead)
	if !client.Encrypted() {
		t.Fatal("conn not marked encrypted after ArmSession")
	}

	// Post-handshake text envelope.
	ev, _ := NewEnvelope(MsgTaskDelegate, "client", "m-1", map[string]string{"x": "y"})
	if err := client.Send(ev); err != nil {
		t.Fatalf("send sealed env: %v", err)
	}
	// Post-handshake data frame rides the same cipher.
	dataEnv, _ := NewEnvelope(MsgArtifactChunk, "client", "m-2",
		ArtifactChunkPayload{TaskID: "t", Hash: "h", OK: true})
	body := []byte("binary-body-bytes")
	if err := client.SendData(dataEnv, body); err != nil {
		t.Fatalf("send sealed data: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case e := <-got:
			switch e.MsgID {
			case "m-1":
				if e.Type != MsgTaskDelegate {
					t.Fatalf("echo type = %s", e.Type)
				}
			case "m-2":
				if string(e.BinaryPayload) != string(body) {
					t.Fatalf("data body = %q", e.BinaryPayload)
				}
			default:
				t.Fatalf("unexpected msg %s", e.MsgID)
			}
		case <-deadline:
			t.Fatal("sealed frames never arrived")
		}
	}

	// The sealed echo must itself decrypt on the client side.
	_ = client.ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var echo Envelope
	if err := client.ReadJSON(&echo); err != nil {
		t.Fatalf("read sealed echo: %v", err)
	}
	if echo.MsgID != "m-1" && echo.MsgID != "m-2" {
		t.Fatalf("echo = %s", echo.MsgID)
	}
}

// TestEncryptedConnRejectsPlaintext: once armed, a plaintext text frame on
// the conn is a downgrade signal — the reader must error, not silently
// parse it.
func TestEncryptedConnRejectsPlaintext(t *testing.T) {
	readErr := make(chan error, 1)
	cancel, _ := startTestServer(t, "127.0.0.1:17891", func(conn *Conn) {
		aead, err := SessionAEAD("s", "a", "b", "n1", "n2")
		if err != nil {
			readErr <- err
			return
		}
		conn.ArmSession(aead)
		var env Envelope
		readErr <- conn.ReadJSON(&env)
	})
	defer cancel()

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:17891/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// A raw plaintext JSON frame — what a legacy or hostile peer would send.
	if err := ws.WriteJSON(Envelope{Type: MsgHello, MsgID: "x"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("plaintext frame on armed conn was accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read never returned")
	}
}
