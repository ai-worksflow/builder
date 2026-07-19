package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	readyFile       = "/tmp/worksflow-gateway-ready"
	maxPorts        = 64
	maxConnections  = 512
	dialTimeout     = 10 * time.Second
	shutdownTimeout = 10 * time.Second
)

var targetPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func main() {
	target := strings.TrimSpace(os.Getenv("WORKSFLOW_GATEWAY_TARGET"))
	ports, err := parsePorts(os.Getenv("WORKSFLOW_GATEWAY_PORTS"))
	if err != nil || !targetPattern.MatchString(target) {
		fmt.Fprintln(os.Stderr, "invalid gateway target or port allowlist")
		os.Exit(64)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	listeners := make([]net.Listener, 0, len(ports))
	for _, port := range ports {
		listener, listenErr := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		if listenErr != nil {
			closeListeners(listeners)
			fmt.Fprintln(os.Stderr, "listen on an allowed port:", listenErr)
			os.Exit(1)
		}
		listeners = append(listeners, listener)
	}
	if err := os.WriteFile(readyFile, []byte("sandbox-gateway/v1\n"), 0o600); err != nil {
		closeListeners(listeners)
		fmt.Fprintln(os.Stderr, "write readiness marker:", err)
		os.Exit(1)
	}

	semaphore := make(chan struct{}, maxConnections)
	var workers sync.WaitGroup
	for index, listener := range listeners {
		workers.Add(1)
		go func(listener net.Listener, port int) {
			defer workers.Done()
			serve(ctx, listener, target, port, semaphore, &workers)
		}(listener, ports[index])
	}
	<-ctx.Done()
	closeListeners(listeners)
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
	}
}

func serve(
	ctx context.Context,
	listener net.Listener,
	target string,
	port int,
	semaphore chan struct{},
	workers *sync.WaitGroup,
) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		select {
		case semaphore <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-semaphore }()
				proxy(ctx, connection, target, port)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func proxy(ctx context.Context, inbound net.Conn, target string, port int) {
	defer inbound.Close()
	dialer := net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	outbound, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(target, strconv.Itoa(port)))
	if err != nil {
		return
	}
	defer outbound.Close()
	var copies sync.WaitGroup
	copies.Add(2)
	go copyHalf(&copies, outbound, inbound)
	go copyHalf(&copies, inbound, outbound)
	copies.Wait()
}

func copyHalf(workers *sync.WaitGroup, destination, source net.Conn) {
	defer workers.Done()
	_, _ = io.Copy(destination, source)
	if tcp, ok := destination.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

func parsePorts(value string) ([]int, error) {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) == 0 || len(parts) > maxPorts {
		return nil, errors.New("invalid port count")
	}
	ports := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, part := range parts {
		port, err := strconv.Atoi(part)
		if err != nil || port < 1024 || port > 65535 || seen[port] {
			return nil, errors.New("invalid port")
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports, nil
}

func closeListeners(listeners []net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}
