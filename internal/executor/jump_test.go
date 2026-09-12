package executor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

func TestSSHExecutor_JumpHostTunnel(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(config.EnvHome, tempDir)
	t.Setenv("OPSPULSE_KNOWN_HOSTS", filepath.Join(tempDir, "known_hosts"))
	t.Setenv("OPSPULSE_TRUST_NEW_HOST_KEY", "1")

	// 1. Start Target Server
	targetListener, targetServer := startMockSSHServer(t, false, "")
	defer func() { _ = targetListener.Close() }()

	// 2. Start Jump Host Server (forwarding to Target Server)
	jumpListener, jumpServer := startMockSSHServer(t, true, targetServer.Address())
	defer func() { _ = jumpListener.Close() }()

	// Configure target server to use jump host
	targetServer.JumpHost = jumpServer.Name

	// 3. Test Execute through Jump Host using ServerResolver
	var warnBuf bytes.Buffer
	exec := NewSSHExecutor().WithWarnWriter(&warnBuf)
	exec.ConnectTimeout = 3 * time.Second
	exec.WithServerResolver(func(name string) (*server.Server, error) {
		if name == jumpServer.Name {
			return &jumpServer, nil
		}
		return nil, fmt.Errorf("server not found: %s", name)
	})

	target := NewServerTarget(targetServer)
	var outputBuf bytes.Buffer
	res, err := exec.Execute(context.Background(), target, "test-task", "echo hello", &outputBuf)
	if err != nil {
		t.Fatalf("Execute through jump host failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(outputBuf.String(), "mock-target-output") {
		t.Errorf("expected output to contain 'mock-target-output', got %q", outputBuf.String())
	}
	if !strings.Contains(warnBuf.String(), "Permanently added") {
		t.Errorf("expected warnBuf to capture host key warning, got %q", warnBuf.String())
	}

	// 4. Test Test() latency probe through Jump Host
	latency, sysInfo, testErr := exec.Test(context.Background(), target)
	if testErr != nil {
		t.Fatalf("Test() through jump host failed: %v", testErr)
	}
	if latency <= 0 {
		t.Errorf("expected positive latency, got %v", latency)
	}
	if !strings.Contains(sysInfo, "mock-target-output") {
		t.Errorf("expected sysInfo to contain 'mock-target-output', got %q", sysInfo)
	}
}

func startMockSSHServer(t *testing.T, isJumpHost bool, forwardToAddr string) (net.Listener, server.Server) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	serverConfig.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	host, portStr, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	name := "target-server"
	if isJumpHost {
		name = "jump-server"
	}

	srv := server.Server{
		Name:     name,
		Host:     host,
		Port:     port,
		User:     "testuser",
		Password: "testpassword",
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleMockConnection(conn, serverConfig, isJumpHost, forwardToAddr)
		}
	}()

	return listener, srv
}

func handleMockConnection(netConn net.Conn, config *ssh.ServerConfig, isJumpHost bool, forwardToAddr string) {
	sshConn, chans, reqs, err := ssh.NewServerConn(netConn, config)
	if err != nil {
		_ = netConn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "direct-tcpip":
			if !isJumpHost {
				_ = newChan.Reject(ssh.Prohibited, "port forwarding disabled")
				continue
			}
			ch, reqs, err := newChan.Accept()
			if err != nil {
				continue
			}
			go ssh.DiscardRequests(reqs)

			destConn, err := net.Dial("tcp", forwardToAddr)
			if err != nil {
				_ = ch.Close()
				continue
			}

			go func() {
				defer func() { _ = ch.Close() }()
				defer func() { _ = destConn.Close() }()
				_, _ = io.Copy(destConn, ch)
			}()
			go func() {
				defer func() { _ = ch.Close() }()
				defer func() { _ = destConn.Close() }()
				_, _ = io.Copy(ch, destConn)
			}()

		case "session":
			ch, reqs, err := newChan.Accept()
			if err != nil {
				continue
			}

			go func() {
				defer func() { _ = ch.Close() }()
				for req := range reqs {
					switch req.Type {
					case "exec":
						_ = req.Reply(true, nil)
						_, _ = ch.Write([]byte("mock-target-output\n"))
						// Send exit status 0
						status := make([]byte, 4)
						binary.BigEndian.PutUint32(status, 0)
						_, _ = ch.SendRequest("exit-status", false, status)
						return
					default:
						_ = req.Reply(false, nil)
					}
				}
			}()

		default:
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel")
		}
	}
	_ = sshConn.Close()
}
