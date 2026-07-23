package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const websocketMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type response struct {
	Project string `json:"project"`
	Host    string `json:"host"`
	Path    string `json:"path"`
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "probe-denied" {
		if err := expectConnectionDenied(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}
	project := strings.TrimSpace(os.Getenv("PROJECT_ID"))
	if project == "" {
		project = "unknown"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/ws", websocketHandler)
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response{Project: project, Host: request.Host, Path: request.URL.Path})
	})
	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	log.Printf("route probe for %s listening on %s", project, server.Addr)
	log.Fatal(server.ListenAndServe())
}

func websocketAccept(key string) string {
	digest := sha1.Sum([]byte(key + websocketMagic))
	return base64.StdEncoding.EncodeToString(digest[:])
}

func websocketHandler(writer http.ResponseWriter, request *http.Request) {
	if !strings.EqualFold(request.Header.Get("Upgrade"), "websocket") ||
		!headerContainsToken(request.Header.Get("Connection"), "upgrade") {
		http.Error(writer, "websocket upgrade required", http.StatusUpgradeRequired)
		return
	}
	key := strings.TrimSpace(request.Header.Get("Sec-WebSocket-Key"))
	if key == "" || request.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(writer, "invalid websocket handshake", http.StatusBadRequest)
		return
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		http.Error(writer, "hijacking unavailable", http.StatusInternalServerError)
		return
	}
	connection, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer connection.Close()
	_, _ = fmt.Fprintf(buffer,
		"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		websocketAccept(key),
	)
	_ = buffer.Flush()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = bufio.NewReader(connection).Peek(1)
}

func headerContainsToken(value string, expected string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), expected) {
			return true
		}
	}
	return false
}

func expectConnectionDenied(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid probe address: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("service DNS resolution failed: %w", err)
	}
	connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(addresses[0], port))
	if err == nil {
		_ = connection.Close()
		return errors.New("cross-namespace connection unexpectedly succeeded")
	}
	log.Printf("cross-namespace connection denied after DNS resolution: %v", err)
	return nil
}
