package ftpengine

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/addictedabhi/airmock/internal/mock"
)

// dataConnTimeout bounds how long a data connection (opened via PASV) waits
// for the client to actually connect before a LIST/RETR/STOR gives up —
// long enough for a real client's own connect delay, short enough that a
// client that never follows through doesn't leak a goroutine forever.
const dataConnTimeout = 10 * time.Second

// session holds the one control connection's state: which PASV listener
// (if any) is currently pending a data connection, and whether login
// succeeded.
type session struct {
	conn         net.Conn
	reader       *bufio.Reader
	def          *mock.Definition
	engine       *Engine
	pendingUser  string
	authed       bool
	pasvListener net.Listener
}

func (e *Engine) handleConn(conn net.Conn, def *mock.Definition) {
	defer conn.Close()
	sess := &session{conn: conn, reader: bufio.NewReader(conn), def: def, engine: e}
	defer sess.closePasvListener()

	if !writeLine(conn, "220 AirMock FTP ready") {
		return
	}
	for {
		line, err := sess.reader.ReadString('\n')
		if err != nil {
			return
		}
		if !sess.handleCommand(strings.TrimRight(line, "\r\n")) {
			return
		}
	}
}

// handleCommand dispatches one command line, returning false once the
// connection should be closed (QUIT, a failed login, or a write failure).
func (s *session) handleCommand(line string) bool {
	cmd, arg := splitCommand(line)
	cfg := s.def.FTP
	switch strings.ToUpper(cmd) {
	case "USER":
		s.pendingUser = arg
		return writeLine(s.conn, "331 Please specify the password")
	case "PASS":
		if cfg.Username == "" || (s.pendingUser == cfg.Username && arg == cfg.Password) {
			s.authed = true
			return writeLine(s.conn, "230 Login successful")
		}
		writeLine(s.conn, "530 Login incorrect")
		return false
	case "PASV", "LIST", "NLST", "RETR", "STOR":
		if !s.authed {
			return writeLine(s.conn, "530 Please login with USER and PASS")
		}
		return s.handleTransferCmd(strings.ToUpper(cmd), arg)
	case "SYST":
		return writeLine(s.conn, "215 UNIX Type: L8")
	case "TYPE":
		return writeLine(s.conn, "200 Switching to "+strings.ToUpper(arg)+" mode")
	case "PWD", "XPWD":
		return writeLine(s.conn, `257 "/" is the current directory`)
	case "CWD", "XCWD":
		return writeLine(s.conn, "250 Directory successfully changed")
	case "NOOP":
		return writeLine(s.conn, "200 OK")
	case "QUIT":
		writeLine(s.conn, "221 Goodbye")
		return false
	default:
		return writeLine(s.conn, "502 Command not implemented")
	}
}

// handleTransferCmd runs the commands that require a completed login.
func (s *session) handleTransferCmd(cmd, arg string) bool {
	switch cmd {
	case "PASV":
		return s.handlePasv()
	case "LIST", "NLST":
		return s.handleList()
	case "RETR":
		return s.handleRetr(arg)
	default:
		return s.handleStor(arg)
	}
}

func (s *session) closePasvListener() {
	if s.pasvListener != nil {
		s.pasvListener.Close()
		s.pasvListener = nil
	}
}

// handlePasv binds a fresh ephemeral listener for the next data transfer and
// tells the client where to connect. PASV's reply format can only carry an
// IPv4 address, so when the control connection arrived over IPv4 the data
// listener binds and advertises that same address — letting a client
// connecting from another machine on the network reach the data connection
// too, not just one on localhost, fixing the previous always-127.0.0.1
// reply. When the control connection arrived over IPv6 (including an IPv6
// loopback dial to an unspecified-address listener), there's no IPv4
// address to honestly report, so this falls back to 127.0.0.1 for both the
// bind and the reply, matching the loopback-only behavior this had before.
func (s *session) handlePasv() bool {
	s.closePasvListener()
	host, _, err := net.SplitHostPort(s.conn.LocalAddr().String())
	if err != nil {
		return writeLine(s.conn, "425 Can't open passive connection")
	}
	bindHost := "127.0.0.1"
	if parsed := net.ParseIP(host); parsed != nil && parsed.To4() != nil {
		bindHost = host
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		return writeLine(s.conn, "425 Can't open passive connection")
	}
	s.pasvListener = ln
	port := ln.Addr().(*net.TCPAddr).Port
	p1, p2 := port/256, port%256
	ip := ln.Addr().(*net.TCPAddr).IP.To4()
	return writeLine(s.conn, fmt.Sprintf("227 Entering Passive Mode (%d,%d,%d,%d,%d,%d)", ip[0], ip[1], ip[2], ip[3], p1, p2))
}

// acceptDataConn consumes the pending PASV listener, blocking (up to
// dataConnTimeout) for the client's data connection.
func (s *session) acceptDataConn() (net.Conn, error) {
	ln := s.pasvListener
	if ln == nil {
		return nil, fmt.Errorf("no PASV listener pending")
	}
	s.pasvListener = nil
	if tcpLn, ok := ln.(*net.TCPListener); ok {
		tcpLn.SetDeadline(time.Now().Add(dataConnTimeout))
	}
	conn, err := ln.Accept()
	ln.Close()
	return conn, err
}

// applyFault runs fault injection right before a transfer's actual data
// (LIST/RETR/STOR — where a mock's "response" conceptually lives for FTP)
// is sent; the handshake commands (USER/PASS/SYST/TYPE/PWD/CWD/NOOP/QUIT)
// aren't part of any configured response and so aren't faulted, matching
// how the HTTP engine only faults the actual response, not request
// handling. Returns false if the connection should be dropped instead of
// continuing — "error" is treated the same as "timeout" (dropped, not a
// substituted FTP reply code), the same choice made for TCP/SMTP.
func (s *session) applyFault() bool {
	switch mock.RollFault(s.def.Fault) {
	case "timeout", "error":
		return false
	}
	if s.def.Fault != nil && s.def.Fault.LatencyJitterMs > 0 {
		time.Sleep(mock.RandomJitter(s.def.Fault.LatencyJitterMs))
	}
	return true
}

func (s *session) handleList() bool {
	data, err := s.acceptDataConn()
	if err != nil {
		return writeLine(s.conn, "425 Can't open data connection")
	}
	defer data.Close()
	if !s.applyFault() {
		return false
	}
	if !writeLine(s.conn, "150 Here comes the directory listing") {
		return false
	}
	for _, f := range s.def.FTP.Files {
		size := f.Size
		if size == 0 {
			size = int64(len(f.Content))
		}
		fmt.Fprintf(data, "-rw-r--r-- 1 ftp ftp %d Jan 01 00:00 %s\r\n", size, f.Name)
	}
	s.engine.recordHit(s.def, "LIST", "", len(s.def.FTP.Files))
	return writeLine(s.conn, "226 Directory send OK")
}

func (s *session) handleRetr(name string) bool {
	file, ok := findFile(s.def.FTP.Files, name)
	if !ok {
		return writeLine(s.conn, "550 Failed to open file")
	}
	data, err := s.acceptDataConn()
	if err != nil {
		return writeLine(s.conn, "425 Can't open data connection")
	}
	if !s.applyFault() {
		data.Close()
		return false
	}
	if !writeLine(s.conn, "150 Opening BINARY mode data connection for "+name) {
		data.Close()
		return false
	}
	data.Write([]byte(file.Content))
	data.Close()
	s.engine.recordHit(s.def, "RETR "+name, "", len(file.Content))
	return writeLine(s.conn, "226 Transfer complete")
}

// handleStor accepts an upload of any filename (not matched against
// FTP.Files — that list is read-only content for LIST/RETR) and logs its
// content to the hit log; nothing is actually persisted anywhere.
func (s *session) handleStor(name string) bool {
	data, err := s.acceptDataConn()
	if err != nil {
		return writeLine(s.conn, "425 Can't open data connection")
	}
	if !s.applyFault() {
		data.Close()
		return false
	}
	if !writeLine(s.conn, "150 Ok to send data") {
		data.Close()
		return false
	}
	content, _ := io.ReadAll(io.LimitReader(data, 10<<20)) // 10MB cap
	data.Close()
	s.engine.recordHit(s.def, "STOR "+name, string(content), len(content))
	return writeLine(s.conn, "226 Transfer complete")
}

func findFile(files []mock.FTPFile, name string) (mock.FTPFile, bool) {
	for _, f := range files {
		if f.Name == name {
			return f, true
		}
	}
	return mock.FTPFile{}, false
}

func splitCommand(line string) (cmd, arg string) {
	line = strings.TrimSpace(line)
	if idx := strings.IndexByte(line, ' '); idx != -1 {
		return line[:idx], line[idx+1:]
	}
	return line, ""
}

func writeLine(conn net.Conn, s string) bool {
	_, err := conn.Write([]byte(s + "\r\n"))
	return err == nil
}
