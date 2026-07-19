package sandbox

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"
)

func TestPTYBinaryFrameRoundTripAndStrictTyping(t *testing.T) {
	frame := PTYBinaryFrame{
		Type: PTYFrameInput, SessionEpoch: 7, Sequence: 2, Ack: 11,
		TerminalID: "11111111-1111-4111-8111-111111111111",
		RequestID:  "22222222-2222-4222-8222-222222222222",
		Payload:    []byte("pnpm dev\r"),
	}
	encoded, err := EncodePTYBinaryFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePTYClientFrame(encoded)
	if err != nil || decoded.Type != frame.Type || decoded.SessionEpoch != frame.SessionEpoch ||
		decoded.Sequence != frame.Sequence || decoded.Ack != frame.Ack || string(decoded.Payload) != string(frame.Payload) {
		t.Fatalf("unexpected frame: %#v err=%v", decoded, err)
	}
	encoded[7] = 0
	if _, err := DecodePTYClientFrame(encoded); err == nil {
		t.Fatal("non-canonical header was accepted")
	}
}

func TestPTYResizeAndOutputFramesAreBounded(t *testing.T) {
	resize := PTYBinaryFrame{
		Type: PTYFrameResize, SessionEpoch: 1, Sequence: 1,
		TerminalID: "11111111-1111-4111-8111-111111111111",
		RequestID:  "22222222-2222-4222-8222-222222222222",
		Payload:    make([]byte, 4),
	}
	binary.BigEndian.PutUint16(resize.Payload[:2], 24)
	binary.BigEndian.PutUint16(resize.Payload[2:], 80)
	encoded, err := EncodePTYBinaryFrame(resize)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePTYClientFrame(encoded); err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(encoded[PTYBinaryHeaderSize:PTYBinaryHeaderSize+2], 1)
	if _, err := DecodePTYClientFrame(encoded); err == nil {
		t.Fatal("invalid terminal size was accepted")
	}

	payload, _ := json.Marshal(map[string]string{
		"terminalId":  "11111111-1111-4111-8111-111111111111",
		"valueBase64": base64.RawStdEncoding.EncodeToString([]byte("ready\r\n")),
	})
	event := StreamEnvelope{
		SchemaVersion: SandboxStreamSchemaVersion,
		SessionID:     "33333333-3333-4333-8333-333333333333", SessionEpoch: 3,
		Channel: ChannelPTY, EventType: "pty.output", Sequence: 9,
		RequestID: "22222222-2222-4222-8222-222222222222", Timestamp: time.Now().UTC(), Payload: payload,
	}
	output, err := EncodePTYServerOutputFrame(event, 4)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePTYFrame(output)
	if err != nil || decoded.Type != PTYFrameOutput || decoded.Sequence != 9 || decoded.Ack != 4 || string(decoded.Payload) != "ready\r\n" {
		t.Fatalf("unexpected output frame: %#v err=%v", decoded, err)
	}
}
