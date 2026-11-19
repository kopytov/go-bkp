package dst

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/textproto"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
)

const (
	// ftpDialTimeout bounds setting up a TCP connection to the server.
	ftpDialTimeout = 30 * time.Second
	// ftpIdleTimeout bounds a read or write on a connection that makes no
	// progress: a server that stopped answering or taking data.
	ftpIdleTimeout = 5 * time.Minute
	// ftpCleanupTimeout bounds deleting the temporary file of a failed
	// upload, which goes on after a signal.
	ftpCleanupTimeout = time.Minute
)

// Ftp is dst class ftp: archives in a directory of an FTP server. Each
// operation logs in over a connection of its own, as the ncftp tools of the
// Raku version do.
type Ftp struct {
	Common   `yaml:",inline"`
	Hostname string `yaml:"hostname"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Path is the directory of the archives, relative to the directory the
	// user logs in to, as in Raku; a leading slash is ignored.
	Path string `yaml:"path"`
	// Encoding is accepted for compatibility and ignored.
	Encoding string `yaml:"encoding"`

	// dialTimeout and idleTimeout are ftpDialTimeout and ftpIdleTimeout,
	// shorter in tests.
	dialTimeout, idleTimeout time.Duration
}

// NewFtp builds an Ftp from its config section.
func NewFtp(n *yaml.Node) (*Ftp, error) {
	f := Ftp{
		Port:        21,
		Username:    "anonymous",
		Password:    "test@test.com",
		dialTimeout: ftpDialTimeout,
		idleTimeout: ftpIdleTimeout,
	}
	// Decoding a key without a value would silently empty its default.
	for _, key := range []string{"hostname", "port", "username", "password", "path", "encoding"} {
		if k, v := keyValue(n, key); v != nil && v.ShortTag() == "!!null" {
			return nil, fmt.Errorf("line %d: %s has no value", k.Line, key)
		}
	}
	if err := config.Decode(n, &f); err != nil {
		return nil, err
	}
	switch {
	case f.Hostname == "":
		return nil, fmt.Errorf("line %d: hostname is required", n.Line)
	case f.Port < 1 || f.Port > 65535:
		return nil, fmt.Errorf("line %d: port %d is out of range 1-65535", n.Line, f.Port)
	case f.Username == "":
		return nil, fmt.Errorf("line %d: username is empty", n.Line)
	case strings.ContainsAny(f.Hostname+f.Username+f.Password+f.Path, "\r\n"):
		// The library refuses such a command; say which attribute it is.
		return nil, fmt.Errorf("line %d: hostname, username, password and path must not hold a line break", n.Line)
	}
	if _, v := keyValue(n, "path"); v == nil {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("line %d: path is not set and the host name is unknown: %w", n.Line, err)
		}
		// As hostname -s gives it.
		f.Path, _, _ = strings.Cut(h, ".")
	}
	if err := f.check(); err != nil {
		return nil, fmt.Errorf("line %d: %w", n.Line, err)
	}
	return &f, nil
}

// Mount implements config.Dest: it logs in and creates path, so that a
// server that cannot be reached fails the attempt before the source starts.
func (f *Ftp) Mount(ctx context.Context) error {
	c, err := f.connect(ctx)
	if err != nil {
		return err
	}
	f.quit(c)
	return nil
}

// Umount implements config.Dest: no connection outlives an operation, so
// there is nothing to release.
func (*Ftp) Umount(context.Context) error { return nil }

// List implements config.Dest: the files in path, without directories.
func (f *Ftp) List(ctx context.Context) ([]config.Entry, error) {
	c, err := f.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer f.quit(c)
	es, err := c.List("")
	if err != nil {
		return nil, f.fail(ctx, "list ftp://"+f.addr(), err)
	}
	out := make([]config.Entry, 0, len(es))
	for _, e := range es {
		if e.Type == ftp.EntryTypeFolder || e.Name == "." || e.Name == ".." {
			continue
		}
		out = append(out, config.Entry{Name: e.Name, Size: int64(min(e.Size, math.MaxInt64))})
	}
	slices.SortFunc(out, func(a, b config.Entry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Delete implements config.Dest. A file that is not there counts as
// deleted, as with local.
func (f *Ftp) Delete(ctx context.Context, name string) error {
	if err := checkBase(name); err != nil {
		return err
	}
	c, err := f.connect(ctx)
	if err != nil {
		return err
	}
	defer f.quit(c)
	if err := c.Delete(name); err != nil && !gone(c, name) {
		return f.fail(ctx, "delete "+name, err)
	}
	return nil
}

// gone reports whether the server says by SIZE that name is not there, to
// tell a refused DELE of a file gone already from one of a file that stays.
// A server without SIZE leaves it unknown, taken as there.
func gone(c *ftp.ServerConn, name string) bool {
	var reply *textproto.Error
	_, err := c.FileSize(name)
	return errors.As(err, &reply) && reply.Code == ftp.StatusFileUnavailable
}

// Open implements config.Dest. Closing the reader ends the session.
func (f *Ftp) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := checkBase(name); err != nil {
		return nil, err
	}
	c, err := f.connect(ctx)
	if err != nil {
		return nil, err
	}
	r, err := c.Retr(name)
	if err != nil {
		f.quit(c)
		return nil, f.fail(ctx, "download "+name, err)
	}
	return &ftpReader{ctx: ctx, f: f, c: c, r: r, name: name}, nil
}

// ftpReader reads a file being downloaded.
type ftpReader struct {
	ctx  context.Context //nolint:containedctx // the context of Open, for the errors of its reads
	f    *Ftp
	c    *ftp.ServerConn
	r    *ftp.Response
	name string
}

// Read reads the file. At the end of the data it reads the server's reply
// to the transfer, as only that tells a complete file from one the server
// broke off, and returns its refusal instead of io.EOF.
func (r *ftpReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	switch {
	case errors.Is(err, io.EOF):
		if cerr := r.r.Close(); cerr != nil {
			return n, r.f.fail(r.ctx, "download "+r.name, cerr)
		}
	case err != nil:
		err = r.f.fail(r.ctx, "download "+r.name, err)
	}
	return n, err
}

// Close reads the server's reply to the transfer and ends the session.
func (r *ftpReader) Close() error {
	err := r.r.Close()
	r.f.quit(r.c)
	if err != nil {
		return r.f.fail(r.ctx, "download "+r.name, err)
	}
	return nil
}

// Write implements config.Dest. The archive is uploaded to
// .<name>.partial while send runs, through a pipe, and renamed to name once
// send and the upload succeeded. A server that does not rename onto an
// existing file gets that file deleted just before. On a failure
// .<name>.partial is deleted over a new connection, as the one of the
// upload may be broken; a failure to do so comes after the error of the
// attempt. It is kept when the rename failed after the archive under name
// was deleted for it, as the only archive left.
func (f *Ftp) Write(ctx context.Context, name string, send func(out *os.File) error) (err error) {
	if err := checkBase(name); err != nil {
		return err
	}
	tmp := "." + name + ".partial"
	// Cancelled when send fails, to break off the upload at once.
	uctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := f.connect(uctx)
	if err != nil {
		return err
	}
	defer f.quit(c)
	// Set once today's earlier archive is deleted to rename onto its
	// name: the temporary file is the only archive of the day then.
	keep := false
	defer func() {
		if err != nil && !keep {
			err = errors.Join(err, f.removePartial(ctx, tmp))
		}
	}()
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	stored := make(chan error, 1)
	go func() {
		// The result goes first: the stages see EPIPE below, and the
		// result must be there once send returns for it.
		stored <- c.Stor(tmp, pr)
		// Should the upload stop early, the stages get EPIPE instead of
		// waiting for a reader that is gone.
		pr.Close() //nolint:errcheck,gosec // read end, nothing to flush
	}()
	sendErr := send(pw)
	// While pw is open the upload cannot have seen the end of the data:
	// if it is over already, it failed, and that is why send failed.
	var storErr error
	storFirst := false
	select {
	case storErr = <-stored:
		storFirst = true
	default:
	}
	if err := pw.Close(); err != nil && sendErr == nil {
		sendErr = fmt.Errorf("close pipe: %w", err)
	}
	if !storFirst {
		if sendErr != nil {
			cancel()
		}
		storErr = <-stored
	}
	switch {
	case storFirst:
		return errors.Join(f.fail(ctx, "upload "+tmp, storErr), sendErr)
	case sendErr != nil:
		return sendErr
	case storErr != nil:
		return f.fail(ctx, "upload "+tmp, storErr)
	}
	deleted, err := rename(c, tmp, name)
	if err != nil {
		if deleted {
			keep = true
			err = fmt.Errorf("%w; %s was deleted to rename onto it, %s is kept", err, name, tmp)
		}
		return f.fail(ctx, "rename "+tmp+" to "+name, err)
	}
	return nil
}

// rename renames from to to. When the server refuses, to is deleted and the
// rename tried again, for a server that does not rename onto an existing
// file; deleted reports that. When to cannot be deleted, such as when it is
// not there, the first refusal is the error.
func rename(c *ftp.ServerConn, from, to string) (deleted bool, err error) {
	err = c.Rename(from, to)
	var reply *textproto.Error
	if err == nil || !errors.As(err, &reply) || reply.Code/100 != 5 {
		return false, err
	}
	if c.Delete(to) != nil {
		return false, err
	}
	return true, c.Rename(from, to)
}

// removePartial deletes tmp over a new connection, within
// ftpCleanupTimeout even when ctx is done. A file the server says is not
// there, as one the failed upload never created, is no error.
func (f *Ftp) removePartial(ctx context.Context, tmp string) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ftpCleanupTimeout)
	defer cancel()
	c, err := f.connect(cctx)
	if err != nil {
		return fmt.Errorf("remove %s: %w", tmp, err)
	}
	defer f.quit(c)
	if err := c.Delete(tmp); err != nil && !gone(c, tmp) {
		return f.fail(cctx, "remove "+tmp, err)
	}
	return nil
}

// addr is the host and port of the server.
func (f *Ftp) addr() string {
	return net.JoinHostPort(f.Hostname, strconv.Itoa(f.Port))
}

// connect logs in and changes to path, creating its directories one by
// one. An error never holds the password.
func (f *Ftp) connect(ctx context.Context) (*ftp.ServerConn, error) {
	c, err := ftp.Dial(f.addr(), ftp.DialWithDialFunc(f.dialer(ctx)))
	if err != nil {
		return nil, f.fail(ctx, "connect to "+f.addr(), err)
	}
	if err := c.Login(f.Username, f.Password); err != nil {
		f.quit(c)
		return nil, f.fail(ctx, fmt.Sprintf("login to %s as %s", f.addr(), f.Username), err)
	}
	if err := f.chdir(c); err != nil {
		f.quit(c)
		return nil, f.fail(ctx, "ftp://"+f.addr(), err)
	}
	return c, nil
}

// chdir changes to path, creating each of its directories. A directory
// the server refuses to create, such as one that exists, is no error by
// itself; one that cannot be changed to is. A failure of the connection
// is.
func (f *Ftp) chdir(c *ftp.ServerConn) error {
	var done []string
	for dir := range strings.SplitSeq(f.Path, "/") {
		if dir == "" || dir == "." {
			continue
		}
		done = append(done, dir)
		var reply *textproto.Error
		if err := c.MakeDir(dir); err != nil && !errors.As(err, &reply) {
			return fmt.Errorf("make %s: %w", strings.Join(done, "/"), err)
		}
		if err := c.ChangeDir(dir); err != nil {
			return fmt.Errorf("change to %s: %w", strings.Join(done, "/"), err)
		}
	}
	return nil
}

// quit ends the session on c. Its reply is not waited for: it tells
// nothing about the operation done before.
func (*Ftp) quit(c *ftp.ServerConn) {
	c.Quit() //nolint:errcheck,gosec // the QUIT reply says nothing about the operation done
}

// fail wraps err of what. When ctx is done the connections were closed for
// it, and its cause is the error instead. The password is replaced by ***
// in the text, should the server have repeated it.
func (f *Ftp) fail(ctx context.Context, what string, err error) error {
	switch {
	case ctx.Err() != nil:
		err = fmt.Errorf("%s: %w", what, context.Cause(ctx))
	case errors.Is(err, os.ErrDeadlineExceeded):
		err = fmt.Errorf("%s: no progress for %v: %w", what, f.idleTimeout, err)
	default:
		err = fmt.Errorf("%s: %w", what, err)
	}
	if f.Password == "" {
		return err
	}
	return redacted{err: err, secret: f.Password}
}

// redacted is an error with secret replaced by *** in its text.
type redacted struct {
	err    error
	secret string
}

func (e redacted) Error() string { return strings.ReplaceAll(e.err.Error(), e.secret, "***") }

func (e redacted) Unwrap() error { return e.err }

// dialer returns how the library connects, to the server and for each data
// transfer: within dialTimeout, with every read and write bounded by
// idleTimeout, and closed once ctx is done. The library itself bounds only
// the first connection, by its dialer, and nothing after it.
func (f *Ftp) dialer(ctx context.Context) func(network, addr string) (net.Conn, error) {
	return func(network, addr string) (net.Conn, error) {
		dctx, cancel := context.WithTimeout(ctx, f.dialTimeout)
		defer cancel()
		var d net.Dialer
		conn, err := d.DialContext(dctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &idleConn{
			Conn: conn,
			idle: f.idleTimeout,
			stop: context.AfterFunc(ctx, func() { conn.Close() }), //nolint:errcheck,gosec // closed to break off whatever it is doing
		}, nil
	}
}

// idleConn is a connection on which a read or write fails once it has made
// no progress for idle.
type idleConn struct {
	net.Conn
	idle time.Duration
	stop func() bool
}

func (c *idleConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func (c *idleConn) Close() error {
	c.stop()
	return c.Conn.Close()
}
