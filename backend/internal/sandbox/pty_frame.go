package sandbox

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"

	"github.com/google/uuid"
)

const (
	PTYBinaryHeaderSize = 64
	ptyBinaryVersion    = 1
	ptyBinaryMaxPayload = 60 << 10
)

var ptyBinaryMagic = [4]byte{'W', 'F', 'P', 'T'}

type PTYFrameType byte

const (
	PTYFrameAttach PTYFrameType = 0x01
	PTYFrameInput  PTYFrameType = 0x02
	PTYFrameResize PTYFrameType = 0x03
	PTYFrameSignal PTYFrameType = 0x04
	PTYFrameDetach PTYFrameType = 0x05

	PTYFrameOutput PTYFrameType = 0x81
)

type PTYBinaryFrame struct {
	Type         PTYFrameType
	SessionEpoch uint64
	Sequence     uint64
	Ack          uint64
	TerminalID   string
	RequestID    string
	Payload      []byte
}

func DecodePTYClientFrame(value []byte) (PTYBinaryFrame, error) {
	frame, err := decodePTYFrame(value)
	if err != nil || frame.Sequence == 0 {
		return PTYBinaryFrame{}, ErrTerminalInvalid
	}
	switch frame.Type {
	case PTYFrameAttach, PTYFrameDetach:
		if len(frame.Payload) != 0 {
			return PTYBinaryFrame{}, ErrTerminalInvalid
		}
	case PTYFrameInput:
		if len(frame.Payload) == 0 {
			return PTYBinaryFrame{}, ErrTerminalInvalid
		}
	case PTYFrameResize:
		if len(frame.Payload) != 4 {
			return PTYBinaryFrame{}, ErrTerminalInvalid
		}
		rows := binary.BigEndian.Uint16(frame.Payload[:2])
		columns := binary.BigEndian.Uint16(frame.Payload[2:])
		if !validTerminalSize(rows, columns) {
			return PTYBinaryFrame{}, ErrTerminalInvalid
		}
	case PTYFrameSignal:
		if !validTerminalSignal(string(frame.Payload)) {
			return PTYBinaryFrame{}, ErrTerminalInvalid
		}
	default:
		return PTYBinaryFrame{}, ErrTerminalInvalid
	}
	return frame, nil
}

func EncodePTYServerOutputFrame(event StreamEnvelope, ack uint64) ([]byte, error) {
	if event.Channel != ChannelPTY || event.EventType != "pty.output" || event.Sequence == 0 ||
		event.SessionEpoch == 0 || !validUUID(event.RequestID) {
		return nil, ErrTerminalInvalid
	}
	var payload struct {
		TerminalID string `json:"terminalId"`
		Value      string `json:"valueBase64"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || !validUUID(payload.TerminalID) {
		return nil, ErrTerminalInvalid
	}
	decoded, err := base64.RawStdEncoding.DecodeString(payload.Value)
	if err != nil || len(decoded) == 0 || len(decoded) > ptyBinaryMaxPayload {
		return nil, ErrTerminalInvalid
	}
	return EncodePTYBinaryFrame(PTYBinaryFrame{
		Type: PTYFrameOutput, SessionEpoch: event.SessionEpoch, Sequence: event.Sequence, Ack: ack,
		TerminalID: payload.TerminalID, RequestID: event.RequestID, Payload: decoded,
	})
}

func EncodePTYBinaryFrame(frame PTYBinaryFrame) ([]byte, error) {
	if frame.SessionEpoch == 0 || frame.Sequence == 0 || !validUUID(frame.TerminalID) ||
		!validUUID(frame.RequestID) || len(frame.Payload) > ptyBinaryMaxPayload {
		return nil, ErrTerminalInvalid
	}
	terminalID, err := uuid.Parse(frame.TerminalID)
	if err != nil {
		return nil, ErrTerminalInvalid
	}
	requestID, err := uuid.Parse(frame.RequestID)
	if err != nil {
		return nil, ErrTerminalInvalid
	}
	value := make([]byte, PTYBinaryHeaderSize+len(frame.Payload))
	copy(value[:4], ptyBinaryMagic[:])
	value[4] = ptyBinaryVersion
	value[5] = byte(frame.Type)
	binary.BigEndian.PutUint16(value[6:8], PTYBinaryHeaderSize)
	binary.BigEndian.PutUint64(value[8:16], frame.SessionEpoch)
	binary.BigEndian.PutUint64(value[16:24], frame.Sequence)
	binary.BigEndian.PutUint64(value[24:32], frame.Ack)
	copy(value[32:48], terminalID[:])
	copy(value[48:64], requestID[:])
	copy(value[PTYBinaryHeaderSize:], frame.Payload)
	return value, nil
}

func decodePTYFrame(value []byte) (PTYBinaryFrame, error) {
	if len(value) < PTYBinaryHeaderSize || len(value) > PTYBinaryHeaderSize+ptyBinaryMaxPayload ||
		value[0] != ptyBinaryMagic[0] || value[1] != ptyBinaryMagic[1] ||
		value[2] != ptyBinaryMagic[2] || value[3] != ptyBinaryMagic[3] ||
		value[4] != ptyBinaryVersion || binary.BigEndian.Uint16(value[6:8]) != PTYBinaryHeaderSize {
		return PTYBinaryFrame{}, ErrTerminalInvalid
	}
	terminalID, err := uuid.FromBytes(value[32:48])
	if err != nil || terminalID.Variant() != uuid.RFC4122 || terminalID.Version() == 0 {
		return PTYBinaryFrame{}, ErrTerminalInvalid
	}
	requestID, err := uuid.FromBytes(value[48:64])
	if err != nil || requestID.Variant() != uuid.RFC4122 || requestID.Version() == 0 {
		return PTYBinaryFrame{}, ErrTerminalInvalid
	}
	frame := PTYBinaryFrame{
		Type: PTYFrameType(value[5]), SessionEpoch: binary.BigEndian.Uint64(value[8:16]),
		Sequence: binary.BigEndian.Uint64(value[16:24]), Ack: binary.BigEndian.Uint64(value[24:32]),
		TerminalID: terminalID.String(), RequestID: requestID.String(),
		Payload: append([]byte(nil), value[PTYBinaryHeaderSize:]...),
	}
	if frame.SessionEpoch == 0 {
		return PTYBinaryFrame{}, ErrTerminalInvalid
	}
	return frame, nil
}

func validTerminalSize(rows, columns uint16) bool {
	return rows >= 2 && rows <= 500 && columns >= 2 && columns <= 500
}

func validTerminalSignal(value string) bool {
	switch value {
	case "INT", "TERM", "KILL", "HUP":
		return true
	default:
		return false
	}
}
