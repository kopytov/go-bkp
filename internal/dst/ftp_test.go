package dst

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/dst/ftptest"
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

// ftpConfig returns the YAML of an ftp dst section for s with path.
func ftpConfig(s *ftptest.Server, path string) string {
	return "{" + s.Attrs(path) + ", plan: {d: 7}, prefix: www}"
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
	s := ftptest.New(t, ftptest.Faults{})
	if err := os.Mkdir(filepath.Join(s.Root, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	// a exists already, b and c do not; the leading slash, a doubled one
	// and . are skipped, as in Raku.
	f := mustFtp(t, ftpConfig(s, "/a//./b/c/"))
	for range 2 { // the second time every directory exists
		if err := f.Mount(t.Context()); err != nil {
			t.Fatalf("Mount: %v", err)
		}
		if !isDir(t, filepath.Join(s.Root, "a", "b", "c")) {
			t.Fatal("a/b/c not created")
		}
	}
	if err := f.Umount(t.Context()); err != nil {
		t.Errorf("Umount: %v", err)
	}

	for _, path := range []string{"", "/", "."} {
		if err := mustFtp(t, ftpConfig(s, path)).Mount(t.Context()); err != nil {
			t.Errorf("Mount with path %q: %v", path, err)
		}
	}
}

func TestFtpMountPathIsFile(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{})
	writeFile(t, filepath.Join(s.Root, "a"), "not a directory")
	err := mustFtp(t, ftpConfig(s, "a/b")).Mount(t.Context())
	if err == nil || !strings.Contains(err.Error(), "change to a: 550") {
		t.Fatalf("Mount error %v, want one about changing to a", err)
	}
}

func TestFtpMountRefused(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{})
	// A user no test server has: should a parallel test take the freed
	// port, the login fails there instead.
	f := mustFtp(t, strings.Replace(ftpConfig(s, ""), "username: "+ftptest.User, "username: nobody", 1))
	s.Stop()
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
		faults ftptest.Faults
		want   string
	}{
		{name: "no greeting", faults: ftptest.Faults{Silent: true}, want: "connect to"},
		{name: "mute after login", faults: ftptest.Faults{Mute: true}, want: "login to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := ftptest.New(t, tt.faults)
			f := mustFtp(t, ftpConfig(s, ""))
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
	s := ftptest.New(t, ftptest.Faults{Silent: true})
	f := mustFtp(t, ftpConfig(s, ""))
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
		s := ftptest.New(t, ftptest.Faults{EchoPass: echo})
		const wrong = "wr0ng-pa55-w0rd"
		f := mustFtp(t, strings.Replace(ftpConfig(s, ""), ftptest.Password, wrong, 1))
		err := f.Mount(t.Context())
		if err == nil || !strings.Contains(err.Error(), "login to 127.0.0.1:") ||
			!strings.Contains(err.Error(), "as "+ftptest.User+": 530") {
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

const ftpArchive = "www-2026-11-19-d.tar"

// ftpFiles returns the names and contents of the files in dir of s.
func ftpFiles(t *testing.T, s *ftptest.Server, dir string) map[string]string {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(s.Root, dir))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, de := range des {
		if !de.IsDir() {
			out[de.Name()] = readFile(t, filepath.Join(s.Root, dir, de.Name()))
		}
	}
	return out
}

func checkFtpFiles(t *testing.T, s *ftptest.Server, dir string, want map[string]string) {
	t.Helper()
	if got := ftpFiles(t, s, dir); !maps.Equal(got, want) {
		t.Errorf("server holds %q, want %q", got, want)
	}
}

// TestFtpCycle writes an archive, lists it, reads it back and deletes it,
// with the listing by MLSD and by LIST.
func TestFtpCycle(t *testing.T) {
	t.Parallel()
	for _, noMLSD := range []bool{false, true} {
		s := ftptest.New(t, ftptest.Faults{NoMLSD: noMLSD})
		f := mustFtp(t, ftpConfig(s, "/backups/www"))
		ctx := t.Context()
		if err := f.Mount(ctx); err != nil {
			t.Fatalf("Mount: %v", err)
		}
		if err := os.Mkdir(filepath.Join(s.Root, "backups", "www", "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(s.Root, "backups", "www", "notes"), "notes")
		if err := f.Write(ctx, ftpArchive, sendString("archive")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		checkFtpFiles(t, s, "backups/www", map[string]string{ftpArchive: "archive", "notes": "notes"})

		es, err := f.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		want := []config.Entry{{Name: "notes", Size: 5}, {Name: ftpArchive, Size: 7}}
		if !slices.Equal(es, want) {
			t.Errorf("List (noMLSD %v) = %v, want %v", noMLSD, es, want)
		}

		r, err := f.Open(ctx, ftpArchive)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if string(data) != "archive" {
			t.Errorf("read %q, want %q", data, "archive")
		}

		if err := f.Delete(ctx, ftpArchive); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		checkFtpFiles(t, s, "backups/www", map[string]string{"notes": "notes"})
	}
}

func TestFtpMissingFile(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{})
	f := mustFtp(t, ftpConfig(s, ""))
	if err := f.Delete(t.Context(), ftpArchive); err != nil {
		t.Errorf("Delete of a file gone already: %v", err)
	}
	if _, err := f.Open(t.Context(), ftpArchive); err == nil || !strings.Contains(err.Error(), "download "+ftpArchive+": 550") {
		t.Errorf("Open error %v, want 550", err)
	}
}

func TestFtpRefusesPath(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{})
	f := mustFtp(t, ftpConfig(s, "a"))
	for _, name := range []string{"", "..", "../x", "a/b"} {
		if err := f.Delete(t.Context(), name); err == nil {
			t.Errorf("Delete(%q) succeeded", name)
		}
		if _, err := f.Open(t.Context(), name); err == nil {
			t.Errorf("Open(%q) succeeded", name)
		}
		if err := f.Write(t.Context(), name, sendString("x")); err == nil {
			t.Errorf("Write(%q) succeeded", name)
		}
	}
}

// TestFtpWriteProcess sends the output of a process, as the pipeline does,
// and checks that the upload ends once the process exits.
func TestFtpWriteProcess(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{})
	f := mustFtp(t, ftpConfig(s, ""))
	const size = 3 << 20
	err := f.Write(t.Context(), ftpArchive, func(out *os.File) error {
		cmd := exec.CommandContext(t.Context(), "head", "-c", strconv.Itoa(size), "/dev/zero")
		cmd.Stdout = out
		return cmd.Run()
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	fi, err := os.Stat(filepath.Join(s.Root, ftpArchive))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != size {
		t.Errorf("archive of %d bytes, want %d", fi.Size(), size)
	}
}

// TestFtpWriteReplaces writes an archive of the same name again, onto a
// server that renames onto an existing file and onto one that does not.
func TestFtpWriteReplaces(t *testing.T) {
	t.Parallel()
	for _, noOverwrite := range []bool{false, true} {
		s := ftptest.New(t, ftptest.Faults{NoOverwrite: noOverwrite})
		f := mustFtp(t, ftpConfig(s, ""))
		for _, data := range []string{"first", "second"} {
			if err := f.Write(t.Context(), ftpArchive, sendString(data)); err != nil {
				t.Fatalf("Write %s (noOverwrite %v): %v", data, noOverwrite, err)
			}
		}
		checkFtpFiles(t, s, "", map[string]string{ftpArchive: "second"})
	}
}

// TestFtpWriteSendFails checks that a failed stage leaves neither the
// temporary file nor a new archive, and the archive of earlier today as it
// was.
func TestFtpWriteSendFails(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{})
	f := mustFtp(t, ftpConfig(s, ""))
	writeFile(t, filepath.Join(s.Root, ftpArchive), "earlier today")
	errSend := errors.New("stage failed")
	err := f.Write(t.Context(), ftpArchive, func(out *os.File) error {
		if _, err := out.WriteString("half"); err != nil {
			t.Error(err)
		}
		return errSend
	})
	if !errors.Is(err, errSend) || err.Error() != errSend.Error() {
		t.Fatalf("Write error %v, want the send error alone", err)
	}
	checkFtpFiles(t, s, "", map[string]string{ftpArchive: "earlier today"})
}

// TestFtpWriteCleanupFails checks that a temporary file that cannot be
// deleted is reported after the error of the attempt.
func TestFtpWriteCleanupFails(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{NoDelete: true})
	f := mustFtp(t, ftpConfig(s, ""))
	errSend := errors.New("stage failed")
	err := f.Write(t.Context(), ftpArchive, func(*os.File) error {
		<-s.Created() // fail once the upload has created the file
		return errSend
	})
	if !errors.Is(err, errSend) {
		t.Fatalf("Write error %v, want the send error", err)
	}
	want := errSend.Error() + "\nremove ." + ftpArchive + ".partial: 550"
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Write error %q, want it to start with %q", err, want)
	}
}

// sendForever writes to out until a write fails, and returns that error.
func sendForever(out *os.File) error {
	buf := make([]byte, 64<<10)
	for {
		if _, err := out.Write(buf); err != nil {
			return err
		}
	}
}

// TestFtpWriteBrokenOff checks that an upload the server drops or stops
// taking fails the attempt, without a hang, and leaves neither the
// temporary file nor the archive.
func TestFtpWriteBrokenOff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		faults ftptest.Faults
		want   string
	}{
		{name: "aborted", faults: ftptest.Faults{AbortAfter: 100 << 10}, want: "upload ." + ftpArchive + ".partial: "},
		{name: "stalled", faults: ftptest.Faults{StallAfter: 100 << 10}, want: "no progress for 200ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := ftptest.New(t, tt.faults)
			f := mustFtp(t, ftpConfig(s, ""))
			f.idleTimeout = 200 * time.Millisecond
			start := time.Now()
			err := f.Write(t.Context(), ftpArchive, sendForever)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Write error %v, want one containing %q", err, tt.want)
			}
			if !errors.Is(err, syscall.EPIPE) {
				t.Errorf("Write error %v, want the send error EPIPE after it", err)
			}
			if strings.Contains(err.Error(), ftptest.Password) {
				t.Errorf("error shows the password: %v", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("Write took %v", d)
			}
			checkFtpFiles(t, s, "", map[string]string{})
		})
	}
}

// TestFtpWriteCancel checks that a done context breaks off an upload the
// server stopped taking, long before the idle timeout, and the temporary
// file is deleted all the same.
func TestFtpWriteCancel(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{StallAfter: 100 << 10})
	f := mustFtp(t, ftpConfig(s, ""))
	f.idleTimeout = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	err := f.Write(ctx, ftpArchive, sendForever)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Write error %v, want context.Canceled", err)
	}
	checkFtpFiles(t, s, "", map[string]string{})
}

// TestFtpDeleteRefused checks that a file the server does not delete is an
// error.
func TestFtpDeleteRefused(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{NoDelete: true})
	f := mustFtp(t, ftpConfig(s, ""))
	writeFile(t, filepath.Join(s.Root, ftpArchive), "old")
	if err := f.Delete(t.Context(), ftpArchive); err == nil || !strings.Contains(err.Error(), "delete "+ftpArchive+": 550") {
		t.Errorf("Delete error %v, want 550", err)
	}
}

// TestFtpWriteRenameRefused checks that when the server neither renames
// onto today's archive nor deletes it, the attempt fails with the refused
// rename, today's archive stays as it was, and the temporary file that
// cannot be deleted either is reported after it.
func TestFtpWriteRenameRefused(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{NoOverwrite: true, NoDelete: true})
	f := mustFtp(t, ftpConfig(s, ""))
	writeFile(t, filepath.Join(s.Root, ftpArchive), "earlier today")
	err := f.Write(t.Context(), ftpArchive, sendString("new"))
	want := "rename ." + ftpArchive + ".partial to " + ftpArchive + `: 553 "file exists"` + "\nremove ." + ftpArchive + ".partial: 550"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("Write error %v, want it to start with %q", err, want)
	}
	checkFtpFiles(t, s, "", map[string]string{ftpArchive: "earlier today", "." + ftpArchive + ".partial": "new"})
}

// TestFtpOpenTruncated checks that a download the server breaks off fails
// at its end instead of looking complete.
func TestFtpOpenTruncated(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{TruncateAfter: 3})
	f := mustFtp(t, ftpConfig(s, ""))
	writeFile(t, filepath.Join(s.Root, ftpArchive), "archive")
	r, err := f.Open(t.Context(), ftpArchive)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, err := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "download "+ftpArchive+": 426") {
		t.Errorf("read %q with error %v, want 426", data, err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestFtpWriteKeepsOnlyCopy checks that when the rename fails after today's
// earlier archive was deleted for it, the new archive stays under its
// temporary name, as it is the only one of the day left.
func TestFtpWriteKeepsOnlyCopy(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{NoRename: true})
	f := mustFtp(t, ftpConfig(s, ""))
	writeFile(t, filepath.Join(s.Root, ftpArchive), "earlier today")
	err := f.Write(t.Context(), ftpArchive, sendString("new"))
	want := ftpArchive + " was deleted to rename onto it, ." + ftpArchive + ".partial is kept"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Write error %v, want one containing %q", err, want)
	}
	checkFtpFiles(t, s, "", map[string]string{"." + ftpArchive + ".partial": "new"})
}
