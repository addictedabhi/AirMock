package tcpengine

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

func TestTCPMockCRLFDelimiterEndToEnd(t *testing.T) {
	// Regression test: LineDelimiter used to be truncated to its first
	// byte, so configuring "\r\n" silently behaved like "\r" and never
	// matched a real CRLF-terminated line.
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "crlf",
		Name:         "crlf-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			LineDelimiter: "\r\n",
			Interactions:  []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG\r\n"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil || resp != "PONG\r\n" {
		t.Fatalf("expected %q, got %q (err=%v)", "PONG\r\n", resp, err)
	}
}

// TestTCPMockCustomDelimiterStillGetsCRLFOnOutput is the regression test
// for a real report: a mock configured with a custom, non-newline
// LineDelimiter (here "###", used only to frame INCOMING lines for this
// mock's own wire protocol) must still terminate its OUTGOING banner and
// responses with a real CRLF — otherwise a human at an interactive telnet
// session never sees a line break at all, since "###" itself never moves a
// terminal's cursor. LineDelimiter and the write-side line ending are two
// independent settings; there is no configuration that couples them.
func TestTCPMockCustomDelimiterStillGetsCRLFOnOutput(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "custom-delim-crlf-output",
		Name:         "custom-delim-crlf-output-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			LineDelimiter: "###",
			Banner:        "220 welcome",
			Interactions:  []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	if got := mustReadLine(t, reader, ""); got != "220 welcome\r\n" {
		t.Fatalf("expected the banner terminated with a real CRLF regardless of the custom delimiter, got %q", got)
	}

	// The custom "###" delimiter must still work for parsing the INCOMING
	// line — a bare '\n' alone (with no "###") must NOT be treated as a
	// complete line.
	if _, err := conn.Write([]byte("PING###")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := mustReadLine(t, reader, ""); got != "PONG\r\n" {
		t.Fatalf("expected the response terminated with a real CRLF (not \"###\"), got %q", got)
	}
}

// TestTCPMockCustomDelimiterSurvivesTrailingCRLFNoise is the regression
// test for a real report: with a custom mid-line LineDelimiter (here ";"),
// a genuine interactive client (telnet, or any terminal in canonical line
// mode) sends a trailing "\r\n" the instant the user presses Enter,
// REGARDLESS of the ";" they also typed as the mock's own delimiter.
// readUntilDelimiter correctly stops the moment it sees ";", so that
// trailing "\r\n" was never consumed — it sat at the front of the
// reader's buffer and got silently glued onto the NEXT line, turning a
// clean "PING" into "\r\nPING" and breaking its exact-match interaction.
// This is why the very first command after connecting always looked
// fine but every one after it didn't: each one carried the previous
// command's leftover "\r\n". Sending a mock's own LOGIN line this way
// made it look like "enabling login" was the trigger, but the bug had
// nothing to do with login — it reproduces with zero login involved, as
// this test shows.
func TestTCPMockCustomDelimiterSurvivesTrailingCRLFNoise(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "custom-delim-crlf-noise",
		Name:         "custom-delim-crlf-noise-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			LineDelimiter:   ";",
			DefaultResponse: "ERR unknown command",
			Interactions: []mock.TCPInteraction{
				{Match: "Hello", MatchType: "exact", Response: "Hii"},
				{Match: "PING", MatchType: "exact", Response: "PONG"},
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	// Every command below is sent exactly the way a real telnet client
	// would: the typed text, the user's own ";" delimiter, THEN the
	// trailing "\r\n" Enter always adds — three in a row, none of them
	// should ever see another command's leftover bytes.
	for i, cmd := range []struct{ send, want string }{
		{"Hello;\r\n", "Hii\r\n"},
		{"PING;\r\n", "PONG\r\n"},
		{"Hello;\r\n", "Hii\r\n"},
	} {
		mustWriteLine(t, conn, cmd.send)
		if got := mustReadLine(t, reader, ""); got != cmd.want {
			t.Fatalf("command %d (%q): expected %q, got %q", i, cmd.send, cmd.want, got)
		}
	}
}

func TestTCPMockLoginGatesInteractions(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "login",
		Name:         "login-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Login: &mock.TCPLoginConfig{
				UsernamePrompt: "Username: \n",
				PasswordPrompt: "Password: \n",
				Username:       "admin",
				Password:       "secret",
				SuccessMessage: "OK\n",
				FailureMessage: "DENIED\n",
				MaxAttempts:    2,
			},
			Interactions: []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG\r\n"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	// Attempt 1: wrong password.
	mustReadLine(t, reader, "Username: \n")
	mustWriteLine(t, conn, "admin\n")
	mustReadLine(t, reader, "Password: \n")
	mustWriteLine(t, conn, "wrong\n")
	if got := mustReadLine(t, reader, ""); got != "DENIED\r\n" {
		t.Fatalf("expected DENIED, got %q", got)
	}

	// Attempt 2: correct credentials.
	mustReadLine(t, reader, "Username: \n")
	mustWriteLine(t, conn, "admin\n")
	mustReadLine(t, reader, "Password: \n")
	mustWriteLine(t, conn, "secret\n")
	if got := mustReadLine(t, reader, ""); got != "OK\r\n" {
		t.Fatalf("expected OK, got %q", got)
	}

	// Now past login, interactions should work normally.
	mustWriteLine(t, conn, "PING\n")
	if got := mustReadLine(t, reader, ""); got != "PONG\r\n" {
		t.Fatalf("expected PONG, got %q", got)
	}
}

// TestTCPMockLineLoginMode covers Login.Mode == "line": the client sends
// its own single-line login command (rather than answering two separate
// interactive prompts) matched against LineFormat to pull out a
// username/password pair.
func TestTCPMockLineLoginMode(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "line-login",
		Name:         "line-login-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			LineDelimiter: "###",
			Login: &mock.TCPLoginConfig{
				Mode:           "line",
				LineFormat:     "LOGIN:{username}:{password}",
				Username:       "admin",
				Password:       "secret",
				SuccessMessage: "LOGGED IN SUCCESSFUL",
				FailureMessage: "DENIED",
				MaxAttempts:    2,
			},
			Interactions: []mock.TCPInteraction{{Match: "Hello", Response: "Hii"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	// Attempt 1: wrong password in the login line.
	if _, err := conn.Write([]byte("LOGIN:admin:wrong###")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := mustReadLine(t, reader, ""); got != "DENIED\r\n" {
		t.Fatalf("expected DENIED, got %q", got)
	}

	// Attempt 2: correct login line.
	if _, err := conn.Write([]byte("LOGIN:admin:secret###")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := mustReadLine(t, reader, ""); got != "LOGGED IN SUCCESSFUL\r\n" {
		t.Fatalf("expected LOGGED IN SUCCESSFUL, got %q", got)
	}

	// Now past login, interactions should work normally, and repeating the
	// exact same command must behave identically both times.
	for i := 0; i < 2; i++ {
		if _, err := conn.Write([]byte("Hello###")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := mustReadLine(t, reader, ""); got != "Hii\r\n" {
			t.Fatalf("iteration %d: expected Hii, got %q", i, got)
		}
	}
}

// TestTCPMockLineLoginModeRejectsNonMatchingLine covers a login line that
// doesn't match LineFormat at all (not just wrong credentials) — treated
// the same as wrong credentials (FailureMessage, another attempt), not a
// crash or a dropped connection.
func TestTCPMockLineLoginModeRejectsNonMatchingLine(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "line-login-nomatch",
		Name:         "line-login-nomatch-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Login: &mock.TCPLoginConfig{
				Mode:           "line",
				LineFormat:     "LOGIN:{username}:{password}",
				Username:       "admin",
				Password:       "secret",
				FailureMessage: "DENIED",
				MaxAttempts:    1,
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	if _, err := conn.Write([]byte("not a login line at all\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := mustReadLine(t, reader, ""); got != "DENIED\r\n" {
		t.Fatalf("expected DENIED for a non-matching line, got %q", got)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("expected the connection to close after MaxAttempts, got %d more bytes: %q", n, buf[:n])
	}
}

// TestTCPMockAutoTerminatesUnterminatedOutput is a regression test for a
// real garbled-terminal bug: a Banner, login SuccessMessage, or Interaction
// Response authored WITHOUT its own trailing line ending (an easy mistake —
// "LOGGED IN SUCCESSFUL" instead of "LOGGED IN SUCCESSFUL\r\n") used to be
// written to the client exactly as typed, so it ran straight into whatever
// was written next on the very same line (observed in the wild as
// "LOGGED IN SUCCESSFULHello" in a real telnet session, the cursor never
// even moving to a new line since a bare "\n" alone isn't guaranteed to —
// see writeEndingFor). Banner/SuccessMessage/Response now always end up on
// their own line, terminated with real CRLF by default, even when the
// configured text itself has no trailing delimiter at all.
func TestTCPMockAutoTerminatesUnterminatedOutput(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "unterminated",
		Name:         "unterminated-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Banner: "220 welcome", // deliberately no trailing \n
			Login: &mock.TCPLoginConfig{
				UsernamePrompt: "Username: ", // deliberately no trailing \n — this one SHOULD stay inline
				PasswordPrompt: "Password: ", // same
				Username:       "admin",
				Password:       "secret",
				SuccessMessage: "LOGGED IN SUCCESSFUL", // deliberately no trailing \n
			},
			Interactions: []mock.TCPInteraction{{Match: "Hello", Response: "Hi"}}, // deliberately no trailing \n
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	// The banner must be readable as its own complete line even though the
	// configured text has no \n — followed immediately by the (correctly
	// still-inline, no added newline) username prompt on the SAME line.
	if got := mustReadLine(t, reader, ""); got != "220 welcome\r\n" {
		t.Fatalf("expected the banner terminated with \\r\\n, got %q", got)
	}

	userPromptBuf := make([]byte, len("Username: "))
	if _, err := io.ReadFull(reader, userPromptBuf); err != nil || string(userPromptBuf) != "Username: " {
		t.Fatalf("expected the username prompt inline (no newline), got %q (err=%v)", userPromptBuf, err)
	}

	mustWriteLine(t, conn, "admin\n")
	// Password prompt follows right after, with no newline of its own yet —
	// read it as a fixed number of bytes rather than up to a delimiter,
	// since there's no line ending to read up to.
	promptBuf := make([]byte, len("Password: "))
	if _, err := io.ReadFull(reader, promptBuf); err != nil || string(promptBuf) != "Password: " {
		t.Fatalf("expected the password prompt inline (no newline), got %q (err=%v)", promptBuf, err)
	}

	mustWriteLine(t, conn, "secret\n")
	if got := mustReadLine(t, reader, ""); got != "LOGGED IN SUCCESSFUL\r\n" {
		t.Fatalf("expected the login success message terminated with \\r\\n, got %q", got)
	}

	mustWriteLine(t, conn, "Hello\n")
	if got := mustReadLine(t, reader, ""); got != "Hi\r\n" {
		t.Fatalf("expected the interaction response terminated with \\r\\n, got %q", got)
	}
}

func TestTCPMockLoginClosesConnectionAfterMaxAttempts(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "login-fail",
		Name:         "login-fail-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Login: &mock.TCPLoginConfig{
				UsernamePrompt: "U:\n",
				PasswordPrompt: "P:\n",
				Username:       "admin",
				Password:       "secret",
				FailureMessage: "DENIED\n",
				MaxAttempts:    1,
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	mustReadLine(t, reader, "U:\n")
	mustWriteLine(t, conn, "admin\n")
	mustReadLine(t, reader, "P:\n")
	mustWriteLine(t, conn, "wrong\n")
	mustReadLine(t, reader, "DENIED\r\n")

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("expected the connection to close after MaxAttempts, got %d more bytes: %q", n, buf[:n])
	}
}

func TestTCPMockSessionTimeoutClosesIdleConnection(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "timeout",
		Name:         "timeout-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			SessionTimeoutSecs: 1,
			DefaultResponse:    "hi\n",
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send nothing and wait past the 1s idle timeout.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 16)
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("expected the server to close the idle connection, got %d bytes: %q", n, buf[:n])
	}
}

func TestTCPMockResponseDelay(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "delay",
		Name:         "delay-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			ResponseDelayMs: 300,
			DefaultResponse: "hi\n",
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	start := time.Now()
	if _, err := conn.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 250*time.Millisecond {
		t.Fatalf("expected the response to be delayed by ~300ms, took only %v", elapsed)
	}
}

type fakeCertProvider struct {
	cert *certs.Certificate
}

func (f *fakeCertProvider) Get(id string) (*certs.Certificate, error) {
	if id != f.cert.ID {
		return nil, fmt.Errorf("not found: %s", id)
	}
	return f.cert, nil
}

func (f *fakeCertProvider) GetBundle(id string) (*certs.Bundle, error) {
	return nil, fmt.Errorf("no bundle: %s", id)
}

func TestTCPMockTLS(t *testing.T) {
	cert, err := certs.Generate(certs.GenerateRequest{
		Name:         "test-ca",
		Kind:         certs.KindCA,
		CommonName:   "airmock-test",
		KeyAlgorithm: certs.KeyECDSA,
		ValidDays:    1,
	})
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	cert.ID = "cert-1"

	e := New()
	e.SetCertProvider(&fakeCertProvider{cert: cert})
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "tls-test",
		Name:         "tls-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			TLS:          &mock.TCPTLSConfig{CertificateID: "cert-1"},
			Interactions: []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG\r\n"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("PING\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil || resp != "PONG\r\n" {
		t.Fatalf("expected %q, got %q (err=%v)", "PONG\r\n", resp, err)
	}
}

func TestTCPMockTLSFailsWithoutCertProvider(t *testing.T) {
	e := New() // no SetCertProvider call
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "tls-no-provider",
		Name:         "tls-no-provider",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP:          &mock.TCPConfig{TLS: &mock.TCPTLSConfig{CertificateID: "whatever"}, Port: 0},
	}
	if err := e.RegisterMock(m); err == nil {
		t.Fatal("expected RegisterMock to fail when TLS is requested but no cert provider is configured")
	}
}

type fakeCertProviderMulti struct {
	byID map[string]*certs.Certificate
}

func (f *fakeCertProviderMulti) Get(id string) (*certs.Certificate, error) {
	c, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("not found: %s", id)
	}
	return c, nil
}

func (f *fakeCertProviderMulti) GetBundle(id string) (*certs.Bundle, error) {
	return nil, fmt.Errorf("no bundle: %s", id)
}

// TestTCPMockMTLSAcceptsClientCertSignedByConfiguredCA guards against a
// real gap: TCP mocks could only do server-side TLS, never mTLS — unlike
// the HTTP gateway, which has always supported RequireClientCert/ClientCAID
// via certs.GatewaySettings. A client presenting a cert signed by the
// mock's configured ClientCAID must be accepted.
func TestTCPMockMTLSAcceptsClientCertSignedByConfiguredCA(t *testing.T) {
	ca, err := certs.Generate(certs.GenerateRequest{Name: "ca", Kind: certs.KindCA, CommonName: "airmock-test-ca", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1})
	if err != nil {
		t.Fatalf("generate ca: %v", err)
	}
	ca.ID = "ca-1"
	clientCert, err := certs.Generate(certs.GenerateRequest{Name: "client", Kind: certs.KindClient, CommonName: "test-client", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1, Issuer: ca})
	if err != nil {
		t.Fatalf("generate client cert: %v", err)
	}

	e := New()
	e.SetCertProvider(&fakeCertProviderMulti{byID: map[string]*certs.Certificate{ca.ID: ca}})
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "mtls-ok", Name: "mtls-ok", Enabled: true, ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			TLS:          &mock.TCPTLSConfig{CertificateID: ca.ID, ClientCertMode: "required", ClientCAID: ca.ID},
			Interactions: []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG\r\n"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	clientTLSCert, err := tls.X509KeyPair([]byte(clientCert.CertPEM), []byte(clientCert.KeyPEM))
	if err != nil {
		t.Fatalf("build client tls cert: %v", err)
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{clientTLSCert}})
	if err != nil {
		t.Fatalf("tls dial with valid client cert: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("PING\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil || resp != "PONG\r\n" {
		t.Fatalf("expected %q, got %q (err=%v)", "PONG\r\n", resp, err)
	}
}

// TestTCPMockMTLSRejectsConnectionWithNoClientCert confirms the flip side:
// a client that presents no certificate at all must be rejected once
// RequireClientCert is on, the same as the HTTP gateway's mTLS already
// guarantees.
func TestTCPMockMTLSRejectsConnectionWithNoClientCert(t *testing.T) {
	ca, err := certs.Generate(certs.GenerateRequest{Name: "ca", Kind: certs.KindCA, CommonName: "airmock-test-ca", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1})
	if err != nil {
		t.Fatalf("generate ca: %v", err)
	}
	ca.ID = "ca-1"

	e := New()
	e.SetCertProvider(&fakeCertProviderMulti{byID: map[string]*certs.Certificate{ca.ID: ca}})
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "mtls-reject", Name: "mtls-reject", Enabled: true, ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			TLS:          &mock.TCPTLSConfig{CertificateID: ca.ID, ClientCertMode: "required", ClientCAID: ca.ID},
			Interactions: []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG\r\n"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return // dial itself failing is an acceptable way for this to manifest
	}
	defer conn.Close()
	// The handshake can succeed at Dial (some TLS stacks defer full
	// verification) but the first read/write should then fail once the
	// server rejects the missing client cert.
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("PING\n")); err == nil {
		if _, err := bufio.NewReader(conn).ReadString('\n'); err == nil {
			t.Fatal("expected the connection to fail without a client certificate")
		}
	}
}

func mustReadLine(t *testing.T, r *bufio.Reader, want string) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	if want != "" && line != want {
		t.Fatalf("expected %q, got %q", want, line)
	}
	return line
}

func mustWriteLine(t *testing.T, conn net.Conn, s string) {
	t.Helper()
	if _, err := conn.Write([]byte(s)); err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
}

// TestTCPMockTLSAppliedViaBundle guards the actual "apply a bundle to a
// mock" feature: a TCPTLSConfig with only BundleID set (no CertificateID/
// ClientCAID of its own) must resolve the bundle's own server cert + CA and
// wire up full mTLS from that alone — using a real *certs.Store (not the
// hand-rolled fakes above) since GetBundle needs genuine bundle storage.
func TestTCPMockTLSAppliedViaBundle(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	certStore := certs.NewStore(db)

	ca, err := certs.Generate(certs.GenerateRequest{Name: "bundle-ca", Kind: certs.KindCA, CommonName: "airmock-bundle-ca", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1})
	if err != nil {
		t.Fatalf("generate ca: %v", err)
	}
	if err := certStore.Save(ca); err != nil {
		t.Fatalf("save ca: %v", err)
	}
	server, err := certs.Generate(certs.GenerateRequest{Name: "bundle-server", Kind: certs.KindServer, CommonName: "127.0.0.1", SANs: []string{"127.0.0.1"}, KeyAlgorithm: certs.KeyECDSA, ValidDays: 1, Issuer: ca})
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}
	if err := certStore.Save(server); err != nil {
		t.Fatalf("save server cert: %v", err)
	}
	clientCert, err := certs.Generate(certs.GenerateRequest{Name: "bundle-client", Kind: certs.KindClient, CommonName: "test-client", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1, Issuer: ca})
	if err != nil {
		t.Fatalf("generate client cert: %v", err)
	}

	bundle, err := certStore.CreateBundle(&certs.Bundle{Name: "test-bundle", CAID: ca.ID, ServerCertID: server.ID, ClientCertID: clientCert.ID})
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}

	e := New()
	e.SetCertProvider(certStore)
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "bundle-mtls", Name: "bundle-mtls", Enabled: true, ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			TLS:          &mock.TCPTLSConfig{BundleID: bundle.ID, ClientCertMode: "required"},
			Interactions: []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG\r\n"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	clientTLSCert, err := tls.X509KeyPair([]byte(clientCert.CertPEM), []byte(clientCert.KeyPEM))
	if err != nil {
		t.Fatalf("build client tls cert: %v", err)
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{clientTLSCert}})
	if err != nil {
		t.Fatalf("tls dial with bundle-issued client cert: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("PING\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil || resp != "PONG\r\n" {
		t.Fatalf("expected %q, got %q (err=%v)", "PONG\r\n", resp, err)
	}

	// A client cert NOT from the bundle's own CA must still be rejected —
	// confirms the bundle's CA (not some other stray trust) is what's
	// actually governing verification.
	otherCA, err := certs.Generate(certs.GenerateRequest{Name: "other-ca", Kind: certs.KindCA, CommonName: "other-ca", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1})
	if err != nil {
		t.Fatalf("generate other ca: %v", err)
	}
	otherClientCert, err := certs.Generate(certs.GenerateRequest{Name: "other-client", Kind: certs.KindClient, CommonName: "other-client", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1, Issuer: otherCA})
	if err != nil {
		t.Fatalf("generate other client cert: %v", err)
	}
	otherTLSCert, err := tls.X509KeyPair([]byte(otherClientCert.CertPEM), []byte(otherClientCert.KeyPEM))
	if err != nil {
		t.Fatalf("build other client tls cert: %v", err)
	}
	badConn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{otherTLSCert}})
	if err == nil {
		defer badConn.Close()
		badConn.SetDeadline(time.Now().Add(1 * time.Second))
		if _, err := bufio.NewReader(badConn).ReadString('\n'); err == nil {
			t.Fatal("expected a client cert from a different CA to be rejected by the bundle's own CA")
		}
	}
}

// TestTCPMockFaultLatencyJitter guards against a real gap: def.Fault was
// only ever read by the HTTP-family matchers — a TCP mock's FaultConfig was
// silently never applied at all, so LatencyJitterMs on a TCP mock did
// nothing.
func TestTCPMockFaultLatencyJitter(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "fault-latency", Name: "fault-latency-test", Enabled: true, ProtocolType: "tcp",
		Fault: &mock.FaultConfig{LatencyJitterMs: 200},
		TCP:   &mock.TCPConfig{Interactions: []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG\n"}}},
	}
	addr := registerOnFreePort(t, e, m)

	// LatencyJitterMs picks a uniformly random delay in [0, 200ms] per hit,
	// so any single trial legitimately can land near zero — assert on the
	// max across several trials instead of one sample, which is what
	// actually distinguishes "jitter is applied" from "jitter is ignored"
	// without being flaky.
	var maxElapsed time.Duration
	for i := 0; i < 8; i++ {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		start := time.Now()
		mustWriteLine(t, conn, "PING\n")
		reader := bufio.NewReader(conn)
		mustReadLine(t, reader, "PONG\r\n")
		if elapsed := time.Since(start); elapsed > maxElapsed {
			maxElapsed = elapsed
		}
		conn.Close()
	}

	if maxElapsed < 50*time.Millisecond {
		t.Fatalf("expected at least one of 8 trials to show noticeable latency jitter (up to 200ms), max observed was %s", maxElapsed)
	}
}

// TestTCPMockFaultTimeoutDropsConnection guards against the same gap for
// the "drop the connection" side of fault injection — TimeoutRatePercent:100
// should mean every interaction just closes the connection with no
// response, not silently ignored.
func TestTCPMockFaultTimeoutDropsConnection(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "fault-timeout", Name: "fault-timeout-test", Enabled: true, ProtocolType: "tcp",
		Fault: &mock.FaultConfig{TimeoutRatePercent: 100},
		TCP:   &mock.TCPConfig{Interactions: []mock.TCPInteraction{{Match: "PING", MatchType: "exact", Response: "PONG\n"}}},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	mustWriteLine(t, conn, "PING\n")
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err == nil {
		t.Fatalf("expected the connection to be dropped with no response, got %d bytes: %q", n, buf[:n])
	}
	if err != io.EOF {
		t.Logf("connection ended with %v (EOF or a reset are both acceptable for a dropped connection)", err)
	}
}
