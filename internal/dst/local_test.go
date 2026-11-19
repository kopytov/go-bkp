package dst

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
)

// newLocal builds a Local from the YAML of a dst section without its class.
func newLocal(t *testing.T, text string) (*Local, error) {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	return NewLocal(doc.Content[0])
}

func mustLocal(t *testing.T, text string) *Local {
	t.Helper()
	l, err := newLocal(t, text)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return l
}

func TestNewLocal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, text, wantErr string
	}{
		{name: "minimal", text: "dir: /srv\nplan: {d: 7}\nprefix: www\n"},
		{name: "full", text: "dir: /mnt/bkp/b\nplan: {d: 7}\nprefix: www\nno-rotate: true\n" +
			"mount: {point: /mnt/bkp, cmd: [sshfs, h:/srv, /mnt/bkp], umount: true}\n"},
		{name: "empty mount", text: "dir: /srv\nplan: {d: 7}\nprefix: www\nmount: {}\n"},
		{name: "no dir", text: "plan: {d: 7}\nprefix: www\n", wantErr: "dir is required"},
		{name: "empty dir", text: "dir: ''\nplan: {d: 7}\nprefix: www\n", wantErr: "dir is required"},
		{name: "no plan", text: "dir: /srv\nprefix: www\n", wantErr: "plan is required"},
		{name: "mount without point", text: "dir: /srv\nplan: {d: 7}\nprefix: www\nmount: {umount: true}\n",
			wantErr: "mount.point is required"},
		{name: "mount with umount false only", text: "dir: /srv\nplan: {d: 7}\nprefix: www\nmount: {umount: false}\n",
			wantErr: "mount.point is required"},
		{name: "mount cmd as string", text: "dir: /srv\nplan: {d: 7}\nprefix: www\n" +
			"mount: {point: /mnt, cmd: sshfs h:/srv /mnt}\n", wantErr: "a command must be a list"},
		{name: "unknown key", text: "dir: /srv\nplan: {d: 7}\nprefix: www\ndirr: /x\n", wantErr: `unknown key "dirr"`},
		{name: "unknown mount key", text: "dir: /srv\nplan: {d: 7}\nprefix: www\nmount: {pont: /mnt}\n",
			wantErr: `unknown key "pont"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := newLocal(t, tt.text)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("NewLocal: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("NewLocal error %v, want one containing %q", err, tt.wantErr)
			}
		})
	}

	l := mustLocal(t, "dir: /srv\nplan: {d: 7}\nprefix: www\nmount: {}\n")
	if l.MountSpec != nil {
		t.Errorf("empty mount section kept as %+v", l.MountSpec)
	}
	l = mustLocal(t, "dir: /srv\nplan: {w: 2, d: 7}\nprefix: www\nno-rotate: true\n")
	if l.Prefix() != "www" {
		t.Errorf("Prefix = %q, want www", l.Prefix())
	}
	want := Namer{Prefix: "www", Suffix: "tar.gz", Plan: Plan{W: 2, D: 7}, NoRotate: true}
	if got := l.Naming("tar.gz"); got != want {
		t.Errorf("Naming = %+v, want %+v", got, want)
	}
}

func localIn(t *testing.T, dir string) *Local {
	t.Helper()
	return mustLocal(t, "dir: "+dir+"\nplan: {d: 7}\nprefix: www\n")
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: a file the test made
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func listNames(t *testing.T, l *Local) []string {
	t.Helper()
	es, err := l.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name)
	}
	return names
}

func sendString(data string) func(*os.File) error {
	return func(out *os.File) error {
		_, err := out.WriteString(data)
		return err
	}
}

func TestLocalWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := localIn(t, dir)
	const name = "www-2026-11-19-d.tar"
	if err := l.Write(t.Context(), name, sendString("archive")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, name)); got != "archive" {
		t.Errorf("archive holds %q, want %q", got, "archive")
	}
	fi, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("archive mode %o, want 600", perm)
	}
	if names := listNames(t, l); !slices.Equal(names, []string{name}) {
		t.Errorf("dir holds %q, want only the archive", names)
	}

	// A second write under the same name replaces it.
	if err := l.Write(t.Context(), name, sendString("again")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, name)); got != "again" {
		t.Errorf("replaced archive holds %q, want %q", got, "again")
	}
}

func TestLocalWriteSendsToPartial(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := localIn(t, dir)
	const name = "www-2026-11-19-d.tar"
	err := l.Write(t.Context(), name, func(out *os.File) error {
		if want := filepath.Join(dir, "."+name+".partial"); out.Name() != want {
			t.Errorf("send got %s, want %s", out.Name(), want)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("archive visible under its name during send: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func TestLocalWriteFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := localIn(t, dir)
	const name = "www-2026-11-19-d.tar"
	writeFile(t, filepath.Join(dir, "www-2026-11-18-d.tar"), "old")
	writeFile(t, filepath.Join(dir, name), "earlier today")
	errSend := errors.New("stage failed")
	err := l.Write(t.Context(), name, func(out *os.File) error {
		if _, err := out.WriteString("half"); err != nil {
			t.Fatal(err)
		}
		return errSend
	})
	if !errors.Is(err, errSend) {
		t.Fatalf("Write error %v, want the send error", err)
	}
	want := []string{"www-2026-11-18-d.tar", name}
	if names := listNames(t, l); !slices.Equal(names, want) {
		t.Errorf("dir holds %q, want %q", names, want)
	}
	if got := readFile(t, filepath.Join(dir, name)); got != "earlier today" {
		t.Errorf("archive of earlier today holds %q after a failed write", got)
	}
}

func TestLocalWriteStalePartial(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := localIn(t, dir)
	const name = "www-2026-11-19-d.tar"
	stale := filepath.Join(dir, "."+name+".partial")
	if err := os.WriteFile(stale, []byte("stale data, longer than the archive"), 0o644); err != nil { //nolint:gosec // G306: a stale file with loose permissions is what is tested
		t.Fatal(err)
	}
	if err := l.Write(t.Context(), name, sendString("new")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, name)); got != "new" {
		t.Errorf("archive holds %q, want %q", got, "new")
	}
}

func TestLocalWriteMissingDir(t *testing.T) {
	t.Parallel()
	l := localIn(t, filepath.Join(t.TempDir(), "gone"))
	called := false
	err := l.Write(t.Context(), "www-2026-11-19-d.tar", func(*os.File) error { called = true; return nil })
	if err == nil || called {
		t.Errorf("Write into a missing dir: error %v, send called %v", err, called)
	}
}

func TestLocalRefusesOutsideNames(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "bkp")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, "victim"), "keep")
	l := localIn(t, dir)
	for _, name := range []string{"", ".", "..", "../victim", "sub/x", "a\x00b"} {
		if err := l.Delete(t.Context(), name); err == nil {
			t.Errorf("Delete accepted %q", name)
		}
		if err := l.Write(t.Context(), name, sendString("x")); err == nil {
			t.Errorf("Write accepted %q", name)
		}
		if f, err := l.Open(t.Context(), name); err == nil {
			f.Close() //nolint:errcheck,gosec // opened by mistake, read-only
			t.Errorf("Open accepted %q", name)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "victim")); err != nil {
		t.Errorf("file outside dir: %v", err)
	}
}

func TestLocalListDelete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := localIn(t, dir)
	writeFile(t, filepath.Join(dir, "b"), "12345")
	writeFile(t, filepath.Join(dir, "a"), "")
	writeFile(t, filepath.Join(dir, ".c.partial"), "1")
	if err := os.Symlink("b", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gone", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	es, err := l.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []config.Entry{{Name: ".c.partial", Size: 1}, {Name: "a", Size: 0}, {Name: "b", Size: 5}, {Name: "link", Size: 5}}
	if !slices.Equal(es, want) {
		t.Errorf("List = %v, want %v", es, want)
	}
	if err := l.Delete(t.Context(), "b"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if names := listNames(t, l); !slices.Equal(names, []string{".c.partial", "a"}) { // link now dangles
		t.Errorf("after Delete dir holds %q", names)
	}
	if err := l.Delete(t.Context(), "b"); err != nil {
		t.Errorf("Delete of a file gone already: %v", err)
	}
}

// fakeTools puts the fake mountpoint, mount and umount first in PATH and
// returns the file their calls are logged to. Tests using it cannot be
// parallel.
func fakeTools(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("testdata", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_TOOLS_LOG", log)
	t.Setenv("FAKE_MOUNT_FAIL", "")
	t.Setenv("FAKE_UMOUNT_FAIL", "")
	return log
}

// calls returns the tool calls logged since the last call and clears the
// log.
func calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log) //nolint:gosec // G304: the log file of the test
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Remove(log); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Fields(strings.ReplaceAll(string(data), " ", "_"))
}

// mountLocal returns a Local whose dir is sub inside the mount point
// point, with the given mount section attributes.
func mountLocal(t *testing.T, point, attrs string) *Local {
	t.Helper()
	return mustLocal(t, "dir: "+point+"/sub\nplan: {d: 7}\nprefix: www\n"+
		"mount: {point: "+point+attrs+"}\n")
}

func mkPoint(t *testing.T) string {
	t.Helper()
	point := t.TempDir()
	if err := os.Mkdir(filepath.Join(point, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	return point
}

func mounted(point string) bool {
	_, err := os.Stat(filepath.Join(point, ".mounted"))
	return err == nil
}

func TestLocalMount(t *testing.T) {
	log := fakeTools(t)
	ctx := t.Context()

	point := mkPoint(t)
	l := mountLocal(t, point, "")
	if err := l.Mount(ctx); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if !mounted(point) {
		t.Error("point not mounted")
	}
	want := []string{"mountpoint_-q_" + point, "mount_" + point, "mountpoint_-q_" + point}
	if got := calls(t, log); !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}

	// Mounted already: nothing to do.
	if err := l.Mount(ctx); err != nil {
		t.Fatalf("second Mount: %v", err)
	}
	if got, want := calls(t, log), []string{"mountpoint_-q_" + point}; !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}

	// mount.cmd instead of mount.
	point = mkPoint(t)
	l = mountLocal(t, point, ", cmd: [touch, "+point+"/.mounted]")
	if err := l.Mount(ctx); err != nil {
		t.Fatalf("Mount with cmd: %v", err)
	}
	if got := calls(t, log); slices.Contains(got, "mount_"+point) || !mounted(point) {
		t.Errorf("mount with cmd: calls %q, mounted %v", got, mounted(point))
	}
}

func TestLocalMountFailures(t *testing.T) {
	fakeTools(t)
	ctx := t.Context()
	tests := []struct {
		name, attrs, wantErr string
		fail                 bool
	}{
		{name: "mount fails", fail: true, wantErr: "can't find in /etc/fstab"},
		{name: "cmd fails", attrs: ", cmd: [sh, -c, 'echo no route >&2; exit 3']", wantErr: "no route"},
		{name: "cmd does not mount", attrs: `, cmd: ["true"]`, wantErr: "not mounted after the mount command"},
		{name: "cmd args stay out of the error", attrs: ", cmd: [sh, -c, 'exit 1', password=s3cret]",
			wantErr: "sh: exit status 1"},
		{name: "cmd missing", attrs: ", cmd: [/nonexistent/sshfs]", wantErr: "/nonexistent/sshfs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fail {
				t.Setenv("FAKE_MOUNT_FAIL", "1")
			}
			point := mkPoint(t)
			err := mountLocal(t, point, tt.attrs).Mount(ctx)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Mount error %v, want one containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("Mount error %v shows the arguments of mount.cmd", err)
			}
			if !strings.Contains(err.Error(), point) {
				t.Errorf("Mount error %v does not name the mount point", err)
			}
		})
	}
}

func TestLocalMountChecksDir(t *testing.T) {
	fakeTools(t)
	ctx := t.Context()
	// dir is on the mounted file system, which is not there.
	point := t.TempDir()
	if err := mountLocal(t, point, "").Mount(ctx); err == nil || !strings.Contains(err.Error(), "sub") {
		t.Errorf("Mount with dir missing after mount: %v", err)
	}
	// Without a mount section.
	if err := localIn(t, filepath.Join(t.TempDir(), "gone")).Mount(ctx); err == nil {
		t.Error("Mount with a missing dir succeeded")
	}
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "")
	if err := localIn(t, file).Mount(ctx); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("Mount with dir a file: %v", err)
	}
}

func TestLocalUmount(t *testing.T) {
	log := fakeTools(t)
	ctx := t.Context()

	point := mkPoint(t)
	l := mountLocal(t, point, ", umount: true")
	if err := l.Mount(ctx); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	calls(t, log)
	if err := l.Umount(ctx); err != nil {
		t.Fatalf("Umount: %v", err)
	}
	if mounted(point) {
		t.Error("still mounted after Umount")
	}
	want := []string{"mountpoint_-q_" + point, "umount_" + point}
	if got := calls(t, log); !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}

	// Not mounted: nothing to do.
	if err := l.Umount(ctx); err != nil {
		t.Fatalf("Umount when not mounted: %v", err)
	}
	if got, want := calls(t, log), []string{"mountpoint_-q_" + point}; !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}

	// Without umount: true the mount stays.
	point = mkPoint(t)
	l = mountLocal(t, point, "")
	if err := l.Mount(ctx); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	calls(t, log)
	if err := l.Umount(ctx); err != nil {
		t.Fatalf("Umount: %v", err)
	}
	if got := calls(t, log); len(got) != 0 || !mounted(point) {
		t.Errorf("Umount without umount: true: calls %q, mounted %v", got, mounted(point))
	}

	// umount fails.
	point = mkPoint(t)
	l = mountLocal(t, point, ", umount: true")
	if err := l.Mount(ctx); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Setenv("FAKE_UMOUNT_FAIL", "1")
	err := l.Umount(ctx)
	if err == nil || !strings.Contains(err.Error(), point) || !strings.Contains(err.Error(), "target is busy") {
		t.Errorf("failed Umount: error %v, want one naming %s with the output of umount", err, point)
	}
	if !mounted(point) {
		t.Error("point not mounted after a failed umount")
	}

	// Without a mount section.
	if err := localIn(t, t.TempDir()).Umount(ctx); err != nil {
		t.Errorf("Umount without mount: %v", err)
	}
}
