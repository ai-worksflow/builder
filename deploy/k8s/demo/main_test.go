package main

import "testing"

func TestWebsocketAccept(t *testing.T) {
	t.Parallel()
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	const expected = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if actual := websocketAccept(key); actual != expected {
		t.Fatalf("websocket accept = %q, want %q", actual, expected)
	}
}

func TestHeaderContainsToken(t *testing.T) {
	t.Parallel()
	if !headerContainsToken("keep-alive, Upgrade", "upgrade") {
		t.Fatal("expected case-insensitive upgrade token")
	}
	if headerContainsToken("keep-alive", "upgrade") {
		t.Fatal("unexpected upgrade token")
	}
}
