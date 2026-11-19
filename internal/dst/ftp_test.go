package dst

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// newFtp builds an Ftp from the YAML of a dst section without its class.
func newFtp(t *testing.T, text string) (*Ftp, error) {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	return NewFtp(doc.Content[0])
}

// mustFtp builds an Ftp from text with short timeouts for tests.
func mustFtp(t *testing.T, text string) *Ftp {
	t.Helper()
	f, err := newFtp(t, text)
	if err != nil {
		t.Fatalf("NewFtp: %v", err)
	}
	f.dialTimeout = 5 * time.Second
	f.idleTimeout = 5 * time.Second
	return f
}

func TestNewFtp(t *testing.T) {
	t.Parallel()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	short, _, _ := strings.Cut(host, ".")
	tests := []struct {
		name, text, wantErr string
		want                Ftp
	}{
		{
			name: "defaults", text: "hostname: h\nplan: {d: 7}\nprefix: www\n",
			want: Ftp{Hostname: "h", Port: 21, Username: "anonymous", Password: "test@test.com", Path: short},
		},
		{
			// README "class: ftp".
			name: "readme ftp",
			text: "hostname: backup.example.com\nusername: bkp\npassword: secret\nport: 21\npath: /backups\n" +
				"encoding: UTF-8\nplan: {d: 7}\nprefix: www\n",
			want: Ftp{
				Hostname: "backup.example.com", Port: 21, Username: "bkp", Password: "secret",
				Path: "/backups", Encoding: "UTF-8",
			},
		},
		{
			// README: the opening example.
			name: "readme full",
			text: "hostname: backup.example.com\nusername: bkpuser\npassword: secret\npath: /backups\n" +
				"plan:\n  m: 6\n  w: 3\n  d: 6\nprefix: www\n",
			want: Ftp{
				Hostname: "backup.example.com", Port: 21, Username: "bkpuser", Password: "secret",
				Path: "/backups",
			},
		},
		{
			name: "empty path and password", text: "hostname: h\npath: ''\npassword: ''\nplan: {d: 7}\nprefix: www\n",
			want: Ftp{Hostname: "h", Port: 21, Username: "anonymous"},
		},
		{name: "no hostname", text: "plan: {d: 7}\nprefix: www\n", wantErr: "hostname is required"},
		{name: "empty hostname", text: "hostname: ''\nplan: {d: 7}\nprefix: www\n", wantErr: "hostname is required"},
		{name: "hostname without value", text: "hostname:\nplan: {d: 7}\nprefix: www\n", wantErr: "hostname has no value"},
		{name: "password without value", text: "hostname: h\npassword: ~\nplan: {d: 7}\nprefix: www\n",
			wantErr: "password has no value"},
		{name: "path without value", text: "hostname: h\npath:\nplan: {d: 7}\nprefix: www\n", wantErr: "path has no value"},
		{name: "port 0", text: "hostname: h\nport: 0\nplan: {d: 7}\nprefix: www\n", wantErr: "port 0 is out of range"},
		{name: "port too big", text: "hostname: h\nport: 65536\nplan: {d: 7}\nprefix: www\n",
			wantErr: "port 65536 is out of range"},
		{name: "port not a number", text: "hostname: h\nport: ftp\nplan: {d: 7}\nprefix: www\n", wantErr: "cannot unmarshal"},
		{name: "empty username", text: "hostname: h\nusername: ''\nplan: {d: 7}\nprefix: www\n", wantErr: "username is empty"},
		{name: "line break", text: "hostname: h\npath: \"a\\r\\nDELE x\"\nplan: {d: 7}\nprefix: www\n",
			wantErr: "must not hold a line break"},
		{name: "no plan", text: "hostname: h\nprefix: www\n", wantErr: "plan is required"},
		{name: "unknown key", text: "hostname: h\nhost: h\nplan: {d: 7}\nprefix: www\n", wantErr: `unknown key "host"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := newFtp(t, tt.text)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NewFtp error %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewFtp: %v", err)
			}
			got := Ftp{
				Hostname: f.Hostname, Port: f.Port, Username: f.Username, Password: f.Password,
				Path: f.Path, Encoding: f.Encoding,
			}
			if got != tt.want {
				t.Errorf("NewFtp = %+v, want %+v", got, tt.want)
			}
			if f.dialTimeout != ftpDialTimeout || f.idleTimeout != ftpIdleTimeout {
				t.Errorf("timeouts %v, %v, want %v, %v", f.dialTimeout, f.idleTimeout, ftpDialTimeout, ftpIdleTimeout)
			}
		})
	}
}

func isDir(t *testing.T, path string) bool {
	t.Helper()
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func TestFtpMountCreatesPath(t *testing.T) {
	t.Parallel()
	s := newFTPServer(t, ftpFaults{})
	if err := os.Mkdir(filepath.Join(s.root, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	// a exists already, b and c do not; the leading slash, a doubled one
	// and . are skipped, as in Raku.
	f := mustFtp(t, s.config("/a//./b/c/"))
	for range 2 { // the second time every directory exists
		if err := f.Mount(t.Context()); err != nil {
			t.Fatalf("Mount: %v", err)
		}
		if !isDir(t, filepath.Join(s.root, "a", "b", "c")) {
			t.Fatal("a/b/c not created")
		}
	}
	if err := f.Umount(t.Context()); err != nil {
		t.Errorf("Umount: %v", err)
	}

	for _, path := range []string{"", "/", "."} {
		if err := mustFtp(t, s.config(path)).Mount(t.Context()); err != nil {
			t.Errorf("Mount with path %q: %v", path, err)
		}
	}
}

func TestFtpMountPathIsFile(t *testing.T) {
	t.Parallel()
	s := newFTPServer(t, ftpFaults{})
	writeFile(t, filepath.Join(s.root, "a"), "not a directory")
	err := mustFtp(t, s.config("a/b")).Mount(t.Context())
	if err == nil || !strings.Contains(err.Error(), "change to a: 550") {
		t.Fatalf("Mount error %v, want one about changing to a", err)
	}
}

func TestFtpMountRefused(t *testing.T) {
	t.Parallel()
	s := newFTPServer(t, ftpFaults{})
	// A user no test server has: should a parallel test take the freed
	// port, the login fails there instead.
	f := mustFtp(t, strings.Replace(s.config(""), "username: "+testFTPUser, "username: nobody", 1))
	s.stop()
	err := f.Mount(t.Context())
	if err == nil || (!strings.Contains(err.Error(), "connect to 127.0.0.1:") &&
		!strings.Contains(err.Error(), "login to 127.0.0.1:")) {
		t.Fatalf("Mount error %v, want one about connecting", err)
	}
}

// TestFtpTimeout checks that a server that stops answering fails the
// operation once nothing moved for the idle timeout.
func TestFtpTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		faults ftpFaults
		want   string
	}{
		{name: "no greeting", faults: ftpFaults{silent: true}, want: "connect to"},
		{name: "mute after login", faults: ftpFaults{mute: true}, want: "login to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newFTPServer(t, tt.faults)
			f := mustFtp(t, s.config(""))
			f.idleTimeout = 200 * time.Millisecond
			start := time.Now()
			err := f.Mount(t.Context())
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "no progress for 200ms") {
				t.Fatalf("Mount error %v, want a timeout in %q", err, tt.want)
			}
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Errorf("Mount error %v does not wrap os.ErrDeadlineExceeded", err)
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("Mount took %v", d)
			}
		})
	}
}

// TestFtpCancel checks that a done context breaks off an operation at once,
// long before the idle timeout.
func TestFtpCancel(t *testing.T) {
	t.Parallel()
	s := newFTPServer(t, ftpFaults{silent: true})
	f := mustFtp(t, s.config(""))
	f.idleTimeout = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := f.Mount(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Mount error %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("Mount took %v", d)
	}
}

// TestFtpPasswordNotInError checks that a refused login does not show the
// password, even when the server repeats it.
func TestFtpPasswordNotInError(t *testing.T) {
	t.Parallel()
	for _, echo := range []bool{false, true} {
		s := newFTPServer(t, ftpFaults{echoPass: echo})
		const wrong = "wr0ng-pa55-w0rd"
		f := mustFtp(t, strings.Replace(s.config(""), testFTPPass, wrong, 1))
		err := f.Mount(t.Context())
		if err == nil || !strings.Contains(err.Error(), "login to 127.0.0.1:") ||
			!strings.Contains(err.Error(), "as "+testFTPUser+": 530") {
			t.Fatalf("Mount error %v, want a refused login", err)
		}
		if strings.Contains(err.Error(), wrong) {
			t.Errorf("error shows the password: %v", err)
		}
		if echo && !strings.Contains(err.Error(), "wrong password ***") {
			t.Errorf("error %v, want the echoed password replaced", err)
		}
	}
}
