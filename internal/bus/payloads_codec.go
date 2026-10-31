// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
)

// Fast-path encoders for the data plane. Two seams in the stock codec burn a
// MiB-scale scan+copy apiece on every chunk (BenchmarkEnvelopeChunkMarshal:
// ~2.8MB alloc/op, ~180MB/s for 1MiB payloads):
//
//  1. json.Marshal compacts every Marshaler's output into its own buffer, so
//     a chunk payload is always base64ed once and then copied+rescanned once.
//     NewEnvelope calls marshalPayload, whose type switch routes the two bulk
//     types to marshalWire — the base64 stream is written straight into the
//     final buffer with no second pass. The methods are plain functions, not
//     the Marshaler interface, precisely so no caller can re-trigger the
//     compact pass.
//  2. json.Marshal(env) does the same to Envelope.Payload (a RawMessage):
//     the already-compact payload gets escaped and copied again. marshalEnvelope
//     marshals the header fields and splices Payload in verbatim — it came
//     from a marshal, so it is compact JSON already.
//
// Wire shape is unchanged: same fields, same quoting — peers on the stock
// codec interoperate, and any code path still calling json.Marshal directly
// produces a decodable frame, just slower.
//
// The decoding side gets the same treatment. A stock json.Unmarshal walks the
// base64 string rune by rune, copies it into a string for base64.DecodeString,
// and pays that twice (Envelope.Payload, then PayloadInto). unmarshalWire finds
// the data member with a byte scan, base64-decodes it in place, and hands only
// the small header to the stock decoder. Anything the fast path cannot prove
// canonical — escapes, duplicate keys, whitespace, malformed base64 — falls
// back to the stock decoder, so acceptance is identical to encoding/json's.

// envelopeHead is Envelope minus Payload, in wire order.
type envelopeHead struct {
	V     int    `json:"v"`
	Type  string `json:"type"`
	MsgID string `json:"msg_id"`
	From  string `json:"from"`
	To    string `json:"to,omitempty"`
	TS    int64  `json:"ts"`
}

// marshalEnvelope encodes env like json.Marshal(env) minus the payload's
// compact pass. The payload RawMessage is spliced in as-is.
func marshalEnvelope(env Envelope) ([]byte, error) {
	head, err := json.Marshal(envelopeHead{
		V: env.V, Type: env.Type, MsgID: env.MsgID,
		From: env.From, To: env.To, TS: env.TS,
	})
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(head)+len(env.Payload)+12)
	out = append(out, head[:len(head)-1]...) // strip the closing }
	if len(env.Payload) > 0 {
		out = append(out, `,"payload":`...)
		out = append(out, env.Payload...)
	}
	out = append(out, '}')
	return out, nil
}

// MarshalEnvelope is marshalEnvelope for callers outside this package that
// put a frame on the wire themselves — transit seals the inner envelope's
// bytes, and an artifact chunk is the one payload big enough that the stock
// marshal's second compact pass shows up in transfer time.
func MarshalEnvelope(env Envelope) ([]byte, error) {
	return marshalEnvelope(env)
}

// marshalPayload is json.Marshal with the bulk-carrier types routed to their
// single-pass encoders. Other types take the stock path.
func marshalPayload(payload any) ([]byte, error) {
	switch p := payload.(type) {
	case ArtifactChunkPayload:
		return p.marshalWire()
	case *ArtifactChunkPayload:
		return p.marshalWire()
	case ArtifactPushPayload:
		return p.marshalWire()
	case *ArtifactPushPayload:
		return p.marshalWire()
	default:
		return json.Marshal(payload)
	}
}

// artifactChunkHead carries every ArtifactChunkPayload field except Data —
// the bulk field is appended by marshalWire. Field order on the wire puts
// data last; object order is not semantically observable and every decoder
// here is field-name driven.
type artifactChunkHead struct {
	TaskID string `json:"task_id"`
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
	Total  int64  `json:"total,omitempty"`
	EOF    bool   `json:"eof,omitempty"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

func (p *ArtifactChunkPayload) marshalWire() ([]byte, error) {
	head, err := json.Marshal(artifactChunkHead{
		TaskID: p.TaskID, Hash: p.Hash, Offset: p.Offset,
		Total: p.Total, EOF: p.EOF, OK: p.OK, Reason: p.Reason,
	})
	if err != nil {
		return nil, err
	}
	return appendB64Field(head, "data", p.Data), nil
}

type artifactPushHead struct {
	TaskID string `json:"task_id"`
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
	Total  int64  `json:"total"`
}

func (p *ArtifactPushPayload) marshalWire() ([]byte, error) {
	head, err := json.Marshal(artifactPushHead{
		TaskID: p.TaskID, Hash: p.Hash, Offset: p.Offset, Total: p.Total,
	})
	if err != nil {
		return nil, err
	}
	return appendB64Field(head, "data", p.Data), nil
}

// appendB64Field splices `,"name":"base64"` onto head (a complete JSON object
// missing nothing), honouring omitempty semantics: empty data emits no field,
// matching the stock codec's treatment of an empty []byte.
func appendB64Field(head []byte, name string, data []byte) []byte {
	out := head[:len(head)-1] // reuse: strip }
	if len(data) > 0 {
		n := base64.StdEncoding.EncodedLen(len(data))
		grown := make([]byte, 0, len(out)+len(name)+n+7)
		out = append(grown, out...)
		out = append(out, `,"`...)
		out = append(out, name...)
		out = append(out, `":"`...)
		out = out[:len(out)+n]
		base64.StdEncoding.Encode(out[len(out)-n:], data)
		out = append(out, '"')
	}
	return append(out, '}')
}

// dataMember is the member the fast decoder lifts out of a bulk payload.
// Both bulk-carrier types name it "data" and encode it as a base64 string.
var dataMember = []byte(`"data":"`)

// splitDataMember returns the payload's header object with the data member
// removed, plus the raw base64 text of that member. ok is false whenever the
// payload is not in the canonical shape this scan can prove it understands;
// callers then use the stock decoder and keep its exact acceptance.
//
// Canonical shape means: base64 uses no escapes, so the value ends at the
// first unescaped quote; the member appears once (json's last-wins rule for
// duplicates would otherwise be silently inverted); and the object is compact,
// so removing the member is a comma trim on the two surrounding fragments.
func splitDataMember(raw []byte) (head, value []byte, ok bool) {
	i := bytes.Index(raw, dataMember)
	if i < 0 {
		return nil, nil, false
	}
	start := i + len(dataMember)
	rel := bytes.IndexByte(raw[start:], '"')
	if rel < 0 {
		return nil, nil, false
	}
	end := start + rel
	value = raw[start:end]
	if bytes.IndexByte(value, '\\') >= 0 {
		return nil, nil, false
	}
	if bytes.Contains(raw[end+1:], dataMember) {
		return nil, nil, false
	}
	pre, post := raw[:i], raw[end+1:]
	switch {
	case len(post) > 0 && post[0] == ',':
		post = post[1:]
	case len(pre) > 0 && pre[len(pre)-1] == ',':
		pre = pre[:len(pre)-1]
	}
	head = make([]byte, 0, len(pre)+len(post))
	head = append(head, pre...)
	head = append(head, post...)
	return head, value, true
}

// decodeB64 decodes the base64 text the fast path found. It allocates exactly
// once — the destination slice the payload keeps.
func decodeB64(value []byte) ([]byte, bool) {
	buf := make([]byte, base64.StdEncoding.DecodedLen(len(value)))
	n, err := base64.StdEncoding.Decode(buf, value)
	if err != nil {
		return nil, false
	}
	return buf[:n], true
}

// unmarshalWire mirrors marshalWire. The data field never passes through the
// stock decoder's string handling; every other field does, from the same
// bytes minus that member.
func (p *ArtifactChunkPayload) unmarshalWire(raw []byte) error {
	head, value, ok := splitDataMember(raw)
	if !ok {
		return json.Unmarshal(raw, p)
	}
	var h artifactChunkHead
	if err := json.Unmarshal(head, &h); err != nil {
		return json.Unmarshal(raw, p)
	}
	data, ok := decodeB64(value)
	if !ok {
		return json.Unmarshal(raw, p)
	}
	*p = ArtifactChunkPayload{
		TaskID: h.TaskID, Hash: h.Hash, Offset: h.Offset, Data: data,
		Total: h.Total, EOF: h.EOF, OK: h.OK, Reason: h.Reason,
	}
	return nil
}

func (p *ArtifactPushPayload) unmarshalWire(raw []byte) error {
	head, value, ok := splitDataMember(raw)
	if !ok {
		return json.Unmarshal(raw, p)
	}
	var h artifactPushHead
	if err := json.Unmarshal(head, &h); err != nil {
		return json.Unmarshal(raw, p)
	}
	data, ok := decodeB64(value)
	if !ok {
		return json.Unmarshal(raw, p)
	}
	*p = ArtifactPushPayload{
		TaskID: h.TaskID, Hash: h.Hash, Offset: h.Offset, Data: data, Total: h.Total,
	}
	return nil
}
