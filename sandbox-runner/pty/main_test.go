package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestParseOptionsAllowsOnlyFixedTerminalShape(t *testing.T) {
	options, err := parseOptions([]string{
		"--id", "11111111-1111-4111-8111-111111111111",
		"--cwd", "frontend", "--rows", "40", "--columns", "120",
	})
	if err != nil || options.workingDirectory != "frontend" || options.rows != 40 || options.columns != 120 {
		t.Fatalf("unexpected options: %#v err=%v", options, err)
	}
	for _, arguments := range [][]string{
		{"--id", "not-a-uuid"},
		{"--id", "11111111-1111-4111-8111-111111111111", "--rows", "1"},
		{"--id", "11111111-1111-4111-8111-111111111111", "--", "/bin/sh"},
	} {
		if _, err := parseOptions(arguments); err == nil {
			t.Fatalf("unsafe options accepted: %#v", arguments)
		}
	}
}

func TestSecureWorkingDirectoryRejectsTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "frontend"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := secureWorkingDirectory(root, "frontend")
	if err != nil || resolved != filepath.Join(root, "frontend") {
		t.Fatalf("unexpected directory: %q err=%v", resolved, err)
	}
	if err := os.Symlink(filepath.Join(root, "frontend"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"../outside", "frontend/../frontend", "linked"} {
		if _, err := secureWorkingDirectory(root, value); err == nil {
			t.Fatalf("unsafe directory accepted: %q", value)
		}
	}
}

func TestReadPacketIsLengthBoundAndTyped(t *testing.T) {
	encoded := packetBytes(packetResize, []byte{0, 24, 0, 80})
	packet, err := readPacket(bytes.NewReader(encoded))
	if err != nil || packet.kind != packetResize || !bytes.Equal(packet.payload, []byte{0, 24, 0, 80}) {
		t.Fatalf("unexpected packet: %#v err=%v", packet, err)
	}
	if _, err := readPacket(bytes.NewReader(packetBytes(packetResize, []byte{0, 24}))); err == nil {
		t.Fatal("short resize packet was accepted")
	}
	oversized := make([]byte, 5)
	oversized[0] = packetInput
	binary.BigEndian.PutUint32(oversized[1:], maxPacketSize+1)
	if _, err := readPacket(bytes.NewReader(oversized)); err == nil {
		t.Fatal("oversized packet was accepted")
	}
	if _, err := readPacket(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func packetBytes(kind byte, payload []byte) []byte {
	value := make([]byte, 5+len(payload))
	value[0] = kind
	binary.BigEndian.PutUint32(value[1:5], uint32(len(payload)))
	copy(value[5:], payload)
	return value
}
