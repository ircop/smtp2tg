// Package smtpd implements a basic SMTP server.
package smtpd

import (
    "log"
    "bufio"
    "bytes"
    "fmt"
    "net"
    "os"
    "regexp"
    "strings"
    "time"
)

var (
    rcptToRE   = regexp.MustCompile(`[Tt][Oo]:(.+)`)
    mailFromRE = regexp.MustCompile(`[Ff][Rr][Oo][Mm]:(.*)`) // Delivery Status Notifications are sent with "MAIL FROM:<>"
    debug = false
)

// Handler function called upon successful receipt of an email.
type Handler func(remoteAddr net.Addr, from string, to []string, data []byte)

// ListenAndServe listens on the TCP network address addr
// and then calls Serve with handler to handle requests
// on incoming connections.
func ListenAndServe(addr string, handler Handler, appname string, hostname string, dbg bool) error {
    debug = dbg
    srv := &Server{Addr: addr, Handler: handler, Appname: appname, Hostname: hostname}
    return srv.ListenAndServe()
}

// Server is an SMTP server.
type Server struct {
    Addr     string // TCP address to listen on, defaults to ":25" (all addresses, port 25) if empty
    Handler  Handler
    Appname  string
    Hostname string
}

// ListenAndServe listens on the TCP network address srv.Addr and then
// calls Serve to handle requests on incoming connections.  If
// srv.Addr is blank, ":25" is used.
func (srv *Server) ListenAndServe() error {
    if srv.Addr == "" {
	srv.Addr = ":25"
    }
    if srv.Appname == "" {
	srv.Appname = "smtpd"
    }
    if srv.Hostname == "" {
	srv.Hostname, _ = os.Hostname()
    }
    ln, err := net.Listen("tcp", srv.Addr)
    if err != nil {
	return err
    }
    return srv.Serve(ln)
}

// Serve creates a new SMTP session after a network connection is established.
func (srv *Server) Serve(ln net.Listener) error {
    defer ln.Close()
    for {
	conn, err := ln.Accept()
	if err != nil {
	    if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
		continue
	    }
	    return err
	}
	session, err := srv.newSession(conn)
	if err != nil {
	    continue
	}
	go session.serve()
    }
}

type session struct {
    srv        *Server
    conn       net.Conn
    br         *bufio.Reader
    bw         *bufio.Writer
    remoteIP   string // Remote IP address
    remoteHost string // Remote hostname according to reverse DNS lookup
    remoteName string // Remote hostname as supplied with EHLO
}

// Create new session from connection.
func (srv *Server) newSession(conn net.Conn) (s *session, err error) {
    s = &session{
	srv:  srv,
	conn: conn,
	br:   bufio.NewReader(conn),
	bw:   bufio.NewWriter(conn),
    }
    return
}

// Function called to handle connection requests.
func (s *session) serve() {
    defer s.conn.Close()
    var from string
    var to []string
    var buffer bytes.Buffer

    // Get remote end info for the Received header.
    s.remoteIP, _, _ = net.SplitHostPort(s.conn.RemoteAddr().String())
    names, err := net.LookupAddr(s.remoteIP)
    if err == nil && len(names) > 0 {
	s.remoteHost = names[0]
    } else {
	s.remoteHost = "unknown"
    }

    Debug( fmt.Sprintf("Incomming connection from %s", s.remoteIP) )

    // Send banner.
    s.writef("220 %s %s SMTP Service ready", s.srv.Hostname, s.srv.Appname)
    
    Debug( fmt.Sprintf("Sent: 220 %s %s SMTP Service ready", s.srv.Hostname, s.srv.Appname) )

loop:
    for {
	// Attempt to read a line from the socket.
	// On error, assume the client has gone away i.e. return from serve().
	line, err := s.readLine()
	if err != nil {
	    break
	}
	verb, args := s.parseLine(line)

	switch verb {
	case "EHLO", "HELO":
	    s.remoteName = args
	    Debug( fmt.Sprintf("Received %s from %s", verb, s.remoteName) )
	    s.writef("250 %s greets %s", s.srv.Hostname, s.remoteName)
	    Debug( fmt.Sprintf("Sent: 250 %s greets %s", s.srv.Hostname, s.remoteName) )

	    // RFC 2821 section 4.1.4 specifies that EHLO has the same effect as RSET.
	    from = ""
	    to = nil
	    buffer.Reset()
	case "MAIL":
	    Debug(fmt.Sprintf("Received MAIL (%s)", args) )
	    match := mailFromRE.FindStringSubmatch(args)
	    if match == nil {
		s.writef("501 Syntax error in parameters or arguments (invalid FROM parameter)")
		log.Printf("[ERR]: 501 Syntax error in parameters or arguments (invalid FROM parameter)")
	    } else {
		from = match[1]
		s.writef("250 Ok")
		Debug("Sent: 250 Ok")
	    }
	    to = nil
	    buffer.Reset()
	case "RCPT":
	    Debug(fmt.Sprintf("Received RCPT (%s)", args) )
	    if from == "" {
		s.writef("503 Bad sequence of commands (MAIL required before RCPT)")
		log.Printf("[ERR]: 503 Bad sequence of commands (MAIL required before RCPT)")
		break
	    }

	    match := rcptToRE.FindStringSubmatch(args)
	    if match == nil {
		s.writef("501 Syntax error in parameters or arguments (invalid TO parameter)")
		log.Printf("[ERR]: 501 Syntax error in parameters or arguments (invalid TO parameter)")
	    } else {
		// RFC 5321 specifies 100 minimum recipients
		if len(to) == 100 {
		    s.writef("452 Too many recipients")
		    log.Printf("[ERR]: 452 Too many recipients")
		} else {
		    to = append(to, match[1])
		    Debug( fmt.Sprintf("to: %s", to) )
		    s.writef("250 Ok")
		    Debug( "Sent: 250 Ok" )
		}
	    }
	case "DATA":
	    if from == "" || to == nil {
		log.Println("[ERR]: 503 Bad sequence of commands (MAIL & RCPT required before DATA)")
		s.writef("503 Bad sequence of commands (MAIL & RCPT required before DATA)")
		break
	    }
	
	    Debug("Sent: 354 Start mail input; end with <CR><LF>.<CR><LF>")
	    s.writef("354 Start mail input; end with <CR><LF>.<CR><LF>")

	    // Attempt to read message body from the socket.
	    // On error, assume the client has gone away i.e. return from serve().
	    data, err := s.readData()
	    if err != nil {
		log.Printf("[ERR]: %s", err.Error())
		break loop
	    }

	    // Create Received header & write message body into buffer.
	    buffer.Reset()
	    buffer.Write(s.makeHeaders(to))
	    buffer.Write(data)
	    Debug("Sent: 250 Ok: queued")
	    s.writef("250 Ok: queued")

	    // Pass mail on to handler.
	    if s.srv.Handler != nil {
		go s.srv.Handler(s.conn.RemoteAddr(), from, to, buffer.Bytes())
	    }

	    // Reset for next mail.
	    from = ""
	    to = nil
	    buffer.Reset()
	case "QUIT":
	    Debug( fmt.Sprintf("221 %s %s SMTP Service closing transmission channel", s.srv.Hostname, s.srv.Appname) )
	    s.writef("221 %s %s SMTP Service closing transmission channel", s.srv.Hostname, s.srv.Appname)
	    break loop
	case "RSET":
	    Debug("RSET. 250 Ok")
	    s.writef("250 Ok")
	    from = ""
	    to = nil
	    buffer.Reset()
	case "NOOP":
	    Debug("NOOP: 250 Ok")
	    s.writef("250 Ok")
	case "HELP", "VRFY", "EXPN":
	    Debug( fmt.Sprintf("Received %s", verb ) )
	    // See RFC 5321 section 4.2.4 for usage of 500 & 502 reply codes
	    s.writef("502 Command not implemented")
	    Debug("Sent: 502 Command not implemented")
	default:
	    // See RFC 5321 section 4.2.4 for usage of 500 & 502 reply codes
	    s.writef("500 Syntax error, command unrecognized")
	    Debug( fmt.Sprintf("Received %s", verb ) )
	    Debug( "Sent: 500 Syntax error, command unrecognized")
	}
    }
}

// Wrapper function for writing a complete line to the socket.
func (s *session) writef(format string, args ...interface{}) {
    fmt.Fprintf(s.bw, format+"\r\n", args...)
    s.bw.Flush()
}

// Read a complete line from the socket.
func (s *session) readLine() (string, error) {
    line, err := s.br.ReadString('\n')
    if err != nil {
	return "", err
    }
    line = strings.TrimSpace(line) // Strip trailing \r\n
    return line, err
}

// Parse a line read from the socket.
func (s *session) parseLine(line string) (verb string, args string) {
    if idx := strings.Index(line, " "); idx != -1 {
	verb = strings.ToUpper(line[:idx])
	args = strings.TrimSpace(line[idx+1:])
    } else {
	verb = strings.ToUpper(line)
	args = ""
    }
    return verb, args
}

// Read the message data following a DATA command.
func (s *session) readData() ([]byte, error) {
    var data []byte
    for {
	line, err := s.br.ReadBytes('\n')
	if err != nil {
	    return nil, err
	}
	// Handle end of data denoted by lone period (\r\n.\r\n)
	if bytes.Equal(line, []byte(".\r\n")) {
	    break
	}
	// Remove leading period (RFC 5321 section 4.5.2)
	if line[0] == '.' {
	    line = line[1:]
	}
	data = append(data, line...)

    }
    return data, nil
}

// Create the Received header to comply with RFC 2821 section 3.8.2.
// TODO: Work out what to do with multiple to addresses.
func (s *session) makeHeaders(to []string) []byte {
    var buffer bytes.Buffer
    now := time.Now().Format("Mon, _2 Jan 2006 15:04:05 -0700 (MST)")
    buffer.WriteString(fmt.Sprintf("Received: from %s (%s [%s])\r\n", s.remoteName, s.remoteHost, s.remoteIP))
    buffer.WriteString(fmt.Sprintf("        by %s (%s) with SMTP\r\n", s.srv.Hostname, s.srv.Appname))
    buffer.WriteString(fmt.Sprintf("        for <%s>; %s\r\n", to[0], now))
    return buffer.Bytes()
}

func Debug(msg string) {
    if( debug == true ) {
	log.Printf( "[DEBUG] %s", msg )
    }
}