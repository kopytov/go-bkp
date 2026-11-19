package dst

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"os"
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
