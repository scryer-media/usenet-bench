package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// probeExternalProvider dials the provider over implicit TLS with the host's
// trust store and returns the status code of its greeting. The greeting text is
// not returned: providers put account and server details in it.
func probeExternalProvider(provider benchmark.ProviderEnv, timeout time.Duration) (string, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", provider.Address(), &tls.Config{
		ServerName: provider.Host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return "", fmt.Errorf("the provider in the provider env does not accept a publicly trusted TLS connection: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("the provider sent no NNTP greeting: %w", err)
	}
	code, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	if code != "200" && code != "201" {
		return "", fmt.Errorf("the provider greeted with status %q, not 200 or 201", code)
	}
	_, _ = conn.Write([]byte("QUIT\r\n"))
	return "status " + code, nil
}
