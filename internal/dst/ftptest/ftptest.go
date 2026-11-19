// Package ftptest provides an FTP server for tests on 127.0.0.1, with
// faults to switch on, in the manner of net/http/httptest.
package ftptest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Faults are what a Server does wrong.
type Faults struct {
	// Silent accepts connections and never answers.
	Silent bool
	// Mute stops answering once the user logged in.
	Mute bool
	// EchoPass repeats a wrong password in the reply to it.
	EchoPass bool
	// NoMLSD leaves MLST out of FEAT, so the client lists with LIST.
	NoMLSD bool
	// NoOverwrite refuses to rename onto an existing file.
	NoOverwrite bool
	// NoDelete refuses DELE.
	NoDelete bool
	// NoRename refuses RNTO.
	NoRename bool
	// StallAfter, when positive, makes STOR stop reading and answering
	// after that many bytes.
	StallAfter int64
	// AbortAfter, when positive, makes STOR drop the data connection
	// after that many bytes and reply 426.
	AbortAfter int64
	// TruncateAfter, when positive, makes RETR end the data after that many
	// bytes and reply 426.
	TruncateAfter int64
}

// Server is an FTP server for tests on 127.0.0.1: user User with
// password Password, the files in Root, which is the user's root, passive
// mode by EPSV only.
type Server struct {
	// Root is the directory the server serves.
	Root     string
	faults   Faults
	ln       net.Listener
	done     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
	// created is closed once a STOR has created its file.
	created     chan struct{}
	createdOnce sync.Once

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// User and Password are the account of every Server.
const (
	User     = "bkp"
	Password = "pa55-w0rd-s3cret"
)

// New starts a Server with root t.TempDir(), stopped at the end of the
// test.
func New(t testing.TB, faults Faults) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Root: t.TempDir(), faults: faults,
		ln: ln, done: make(chan struct{}), created: make(chan struct{}), conns: map[net.Conn]struct{}{},
	}
	s.wg.Go(s.accept)
	t.Cleanup(s.Stop)
	return s
}

// Created is closed once a STOR has created its file.
func (s *Server) Created() <-chan struct{} { return s.created }

// Port is the port the server listens on.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Stop closes every connection and waits for the sessions. It may be
// called more than once.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		close(s.done)
		s.ln.Close() //nolint:errcheck,gosec // test server shutdown
		s.mu.Lock()
		for c := range s.conns {
			c.Close() //nolint:errcheck,gosec // test server shutdown
		}
		s.mu.Unlock()
	})
	s.wg.Wait()
}

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		if !s.track(c) {
			return
		}
		s.wg.Go(func() {
			defer s.untrack(c)
			s.serve(c)
		})
	}
}

// track registers c, control or data connection, to be closed by stop. It
// closes c and returns false when the server is stopped already.
func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done: // stop has closed the others already
		c.Close() //nolint:errcheck,gosec // test server shutdown
		return false
	default:
	}
	s.conns[c] = struct{}{}
	return true
}

// untrack closes c and forgets it.
func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	c.Close() //nolint:errcheck,gosec // done with it
}

// session is the state of one control connection.
type session struct {
	s      *Server
	c      net.Conn
	cwd    string // slash-separated, from the root
	user   string
	logged bool
	data   net.Listener // opened by EPSV, for the next transfer
	rnfr   string
}

func (s *Server) serve(c net.Conn) {
	if s.faults.Silent {
		<-s.done
		return
	}
	ss := &session{s: s, c: c, cwd: "/"}
	defer ss.closeData()
	ss.reply("220 test server ready")
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if s.faults.Mute && ss.logged {
			<-s.done
			return
		}
		cmd, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		if !ss.handle(strings.ToUpper(cmd), arg) {
			return
		}
	}
}

func (ss *session) reply(format string, args ...any) {
	fmt.Fprintf(ss.c, format+"\r\n", args...) //nolint:errcheck // a write to a gone client fails the next read
}

// local returns the file name of the server path name, taken from cwd.
// Cleaning it from the root keeps it inside the root.
func (ss *session) local(name string) (string, string) {
	p := path.Join("/", ss.cwd, name)
	if strings.HasPrefix(name, "/") {
		p = path.Clean(name)
	}
	return p, filepath.Join(ss.s.Root, filepath.FromSlash(p))
}

// handle runs one command and reports whether the session goes on.
func (ss *session) handle(cmd, arg string) bool {
	switch cmd {
	case "USER":
		ss.user, ss.logged = arg, false
		ss.reply("331 password please")
		return true
	case "PASS":
		switch {
		case ss.user == User && arg == Password:
			ss.logged = true
			ss.reply("230 logged in")
		case ss.s.faults.EchoPass:
			ss.reply("530 wrong password %s for %s", arg, ss.user)
		default:
			ss.reply("530 login incorrect")
		}
		return true
	case "QUIT":
		ss.reply("221 bye")
		return false
	}
	if !ss.logged {
		ss.reply("530 not logged in")
		return true
	}
	switch cmd {
	case "FEAT":
		mlst := " MLST type*;size*;\r\n"
		if ss.s.faults.NoMLSD {
			mlst = ""
		}
		ss.reply("211-features:\r\n EPSV\r\n UTF8\r\n%s211 end", mlst)
	case "TYPE", "OPTS", "NOOP":
		ss.reply("200 ok")
	case "PWD":
		ss.reply("257 %q", ss.cwd)
	case "CWD":
		p, l := ss.local(arg)
		if fi, err := os.Stat(l); err != nil || !fi.IsDir() {
			ss.reply("550 no such directory")
			return true
		}
		ss.cwd = p
		ss.reply("250 ok")
	case "MKD":
		_, l := ss.local(arg)
		if err := os.Mkdir(l, 0o700); err != nil {
			ss.reply("550 cannot create")
			return true
		}
		ss.reply("257 created")
	case "EPSV":
		ss.closeData()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			ss.reply("425 %v", err)
			return true
		}
		ss.data = ln
		ss.reply("229 entering extended passive mode (|||%d|)", ln.Addr().(*net.TCPAddr).Port)
	case "MLSD", "LIST":
		ss.list(cmd == "MLSD")
	case "STOR":
		ss.stor(arg)
	case "RETR":
		ss.retr(arg)
	case "DELE":
		if ss.s.faults.NoDelete {
			ss.reply("550 permission denied")
			return true
		}
		_, l := ss.local(arg)
		if fi, err := os.Stat(l); err != nil || !fi.Mode().IsRegular() {
			ss.reply("550 no such file")
			return true
		}
		if err := os.Remove(l); err != nil {
			ss.reply("550 cannot delete")
			return true
		}
		ss.reply("250 deleted")
	case "SIZE":
		_, l := ss.local(arg)
		fi, err := os.Stat(l)
		if err != nil || !fi.Mode().IsRegular() {
			ss.reply("550 no such file")
			return true
		}
		ss.reply("213 %d", fi.Size())
	case "RNFR":
		_, l := ss.local(arg)
		if _, err := os.Stat(l); err != nil {
			ss.reply("550 no such file")
			return true
		}
		ss.rnfr = l
		ss.reply("350 ready for RNTO")
	case "RNTO":
		from := ss.rnfr
		ss.rnfr = ""
		_, to := ss.local(arg)
		if from == "" {
			ss.reply("503 RNFR first")
			return true
		}
		if ss.s.faults.NoRename {
			ss.reply("553 rename refused")
			return true
		}
		if _, err := os.Stat(to); err == nil && ss.s.faults.NoOverwrite {
			ss.reply("553 file exists")
			return true
		}
		if err := os.Rename(from, to); err != nil {
			ss.reply("553 cannot rename")
			return true
		}
		ss.reply("250 renamed")
	default:
		ss.reply("502 not implemented")
	}
	return true
}

func (ss *session) closeData() {
	if ss.data != nil {
		ss.data.Close() //nolint:errcheck,gosec // listener for one transfer
		ss.data = nil
	}
}

// openData accepts the data connection of the transfer EPSV announced.
func (ss *session) openData() (net.Conn, error) {
	ln := ss.data
	if ln == nil {
		return nil, errors.New("no EPSV")
	}
	defer ss.closeData()
	if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}
	dc, err := ln.Accept()
	if err != nil {
		return nil, err
	}
	if !ss.s.track(dc) {
		return nil, errors.New("server stopped")
	}
	return dc, nil
}

func (ss *session) list(mlsd bool) {
	_, l := ss.local("")
	des, err := os.ReadDir(l)
	if err != nil {
		ss.closeData()
		ss.reply("550 cannot list")
		return
	}
	dc, err := ss.openData()
	if err != nil {
		ss.reply("425 %v", err)
		return
	}
	ss.reply("150 here it comes")
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		switch {
		case mlsd && de.IsDir():
			fmt.Fprintf(dc, "type=dir; %s\r\n", de.Name()) //nolint:errcheck // the client sees a short listing
		case mlsd:
			fmt.Fprintf(dc, "type=file;size=%d; %s\r\n", fi.Size(), de.Name()) //nolint:errcheck // as above
		case de.IsDir():
			fmt.Fprintf(dc, "drwx------ 2 bkp bkp 4096 Nov 19 10:00 %s\r\n", de.Name()) //nolint:errcheck // as above
		default:
			fmt.Fprintf(dc, "-rw------- 1 bkp bkp %d Nov 19 10:00 %s\r\n", fi.Size(), de.Name()) //nolint:errcheck // as above
		}
	}
	ss.s.untrack(dc) // the listing is sent
	ss.reply("226 done")
}

func (ss *session) stor(name string) {
	_, l := ss.local(name)
	dc, err := ss.openData()
	if err != nil {
		ss.reply("425 %v", err)
		return
	}
	defer ss.s.untrack(dc)
	f, err := os.Create(l) //nolint:gosec // G304: a path under the test root
	if err != nil {
		ss.reply("553 cannot create")
		return
	}
	defer f.Close() //nolint:errcheck // closed again on the normal path
	ss.s.createdOnce.Do(func() { close(ss.s.created) })
	ss.reply("150 send it")
	switch faults := ss.s.faults; {
	case faults.StallAfter > 0:
		io.CopyN(f, dc, faults.StallAfter) //nolint:errcheck,gosec // whatever came before the stall
		<-ss.s.done
		return
	case faults.AbortAfter > 0:
		io.CopyN(f, dc, faults.AbortAfter) //nolint:errcheck,gosec // whatever came before the abort
		ss.s.untrack(dc)                   // the abort itself
		f.Close()                          //nolint:errcheck,gosec // the partial file stays
		ss.reply("426 connection closed; transfer aborted")
		return
	}
	if _, err := io.Copy(f, dc); err != nil {
		ss.reply("426 %v", err)
		return
	}
	if err := f.Close(); err != nil {
		ss.reply("451 %v", err)
		return
	}
	ss.reply("226 stored")
}

func (ss *session) retr(name string) {
	_, l := ss.local(name)
	f, err := os.Open(l) //nolint:gosec // G304: a path under the test root
	if err != nil {
		ss.closeData()
		ss.reply("550 no such file")
		return
	}
	defer f.Close() //nolint:errcheck // read only
	dc, err := ss.openData()
	if err != nil {
		ss.reply("425 %v", err)
		return
	}
	ss.reply("150 here it comes")
	if n := ss.s.faults.TruncateAfter; n > 0 {
		io.CopyN(dc, f, n) //nolint:errcheck,gosec // whatever goes before the break
		ss.s.untrack(dc)
		ss.reply("426 connection closed; transfer aborted")
		return
	}
	_, err = io.Copy(dc, f)
	ss.s.untrack(dc) // end of the file for the client
	if err != nil {
		ss.reply("426 %v", err)
		return
	}
	ss.reply("226 sent")
}

// Attrs returns the attributes of an ftp dst section for s with path, as
// the inside of a YAML flow mapping.
func (s *Server) Attrs(path string) string {
	return "hostname: 127.0.0.1, port: " + strconv.Itoa(s.Port()) + ", username: " + User +
		", password: " + Password + ", path: '" + path + "'"
}
