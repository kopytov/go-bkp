package run

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// downloadHarness makes a config of profiles db and www sharing a
// destination with an archive of each, a temporary file and a file that is
// not an archive, a config of one source, and a directory to download to.
type downloadHarness struct {
	multi, single, out string
}

func newDownloadHarness(t *testing.T) downloadHarness {
	t.Helper()
	h := newHarness(t)
	shared := h.dst(t, "shared")
	writeFiles(t, shared, map[string]string{
		"db-2026-11-19-d.txt":           "db archive",
		"www-2026-11-19-d.txt":          "www archive",
		".www-2026-11-20-d.txt.partial": "half",
		"notes.txt":                     "notes",
	})
	own := h.dst(t, "own")
	writeFiles(t, own, map[string]string{"single-2026-11-19-d.txt": "single archive"})
	return downloadHarness{
		multi: h.config(t, "multi.yml", `src:
  www: {class: cmd, cmd: [printf, w], suffix: txt}
  db: {class: cmd, cmd: [printf, d], suffix: txt}
dst: {class: local, dir: `+shared+`, plan: {d: 3}}
`),
		single: h.config(t, "single.yml", cmdConfig("printf s", own, "")),
		out:    h.dst(t, "out"),
	}
}

func TestDownloadToFile(t *testing.T) {
	t.Parallel()
	h := newDownloadHarness(t)
	target := filepath.Join(h.out, "got")
	d := Download{Config: h.multi, Archive: "www-2026-11-19-d.txt", Profile: "www", Filename: target}
	var stdout bytes.Buffer
	if err := d.Run(t.Context(), Classes(), &stdout); err != nil {
		t.Fatal(err)
	}
	checkFiles(t, h.out, map[string]string{"got": "www archive"})
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %o, want 600", perm)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout %q, want nothing", &stdout)
	}
}

func TestDownloadToStdout(t *testing.T) {
	t.Parallel()
	h := newDownloadHarness(t)
	var stdout bytes.Buffer
	d := Download{Config: h.single, Archive: "single-2026-11-19-d.txt", Filename: "-"}
	if err := d.Run(t.Context(), Classes(), &stdout); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "single archive" {
		t.Errorf("stdout %q", got)
	}
	checkFiles(t, h.out, map[string]string{})
}

func TestDownloadExisting(t *testing.T) {
	t.Parallel()
	h := newDownloadHarness(t)
	target := filepath.Join(h.out, "got")
	writeFiles(t, h.out, map[string]string{"got": "old"})
	d := Download{Config: h.single, Archive: "single-2026-11-19-d.txt", Filename: target}
	err := d.Run(t.Context(), Classes(), nil)
	if err == nil || err.Error() != target+" exists, use --force option" {
		t.Errorf("error %v, want that the file exists", err)
	}
	checkFiles(t, h.out, map[string]string{"got": "old"})

	if err := os.Chmod(target, 0o644); err != nil { //nolint:gosec // G302: a wider mode the download must not keep
		t.Fatal(err)
	}
	d.Force = true
	if err := d.Run(t.Context(), Classes(), nil); err != nil {
		t.Fatal(err)
	}
	checkFiles(t, h.out, map[string]string{"got": "single archive"})
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %o after --force, want 600", perm)
	}
}

// TestDownloadExistingAfterCopy has the file appear while the archive is
// copied: it stays, and the copy goes.
func TestDownloadExistingAfterCopy(t *testing.T) {
	t.Parallel()
	h := newDownloadHarness(t)
	target := filepath.Join(h.out, "got")
	r := &appearingReader{r: strings.NewReader("new"), path: target}
	err := writeFile(target, false, r)
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("error %v, want that the file exists", err)
	}
	checkFiles(t, h.out, map[string]string{"got": "appeared"})
}

// appearingReader writes path at its first read.
type appearingReader struct {
	r    *strings.Reader
	path string
	done bool
}

func (a *appearingReader) Read(p []byte) (int, error) {
	if !a.done {
		a.done = true
		if err := os.WriteFile(a.path, []byte("appeared"), 0o600); err != nil {
			return 0, err
		}
	}
	return a.r.Read(p)
}

func TestDownloadDefaultFilename(t *testing.T) {
	h := newDownloadHarness(t)
	t.Chdir(h.out)
	d := Download{Config: h.single, Archive: "single-2026-11-19-d.txt"}
	if err := d.Run(t.Context(), Classes(), nil); err != nil {
		t.Fatal(err)
	}
	checkFiles(t, h.out, map[string]string{"single-2026-11-19-d.txt": "single archive"})
}

func TestDownloadErrors(t *testing.T) {
	t.Parallel()
	h := newDownloadHarness(t)
	tests := []struct {
		name string
		d    Download
		want string
	}{
		{"several profiles", Download{Config: h.multi, Archive: "www-2026-11-19-d.txt"}, "--profile should be specified for " + h.multi},
		{"unknown profile", Download{Config: h.multi, Archive: "www-2026-11-19-d.txt", Profile: "nope"}, "profile nope not known in " + h.multi},
		{"archive of another profile", Download{Config: h.multi, Archive: "db-2026-11-19-d.txt", Profile: "www"}, "profile www: no archive db-2026-11-19-d.txt"},
		{"temporary file", Download{Config: h.multi, Archive: ".www-2026-11-20-d.txt.partial", Profile: "www"}, "no archive .www-2026-11-20-d.txt.partial"},
		{"not an archive", Download{Config: h.multi, Archive: "notes.txt", Profile: "www"}, "no archive notes.txt"},
		{"outside", Download{Config: h.single, Archive: "../shared/www-2026-11-19-d.txt"}, "no archive ../shared/"},
		{"missing", Download{Config: h.single, Archive: "single-2026-11-18-d.txt"}, "no archive single-2026-11-18-d.txt"},
		{"missing config", Download{Config: h.multi + ".none", Archive: "x"}, "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.d.Filename = filepath.Join(h.out, strings.ReplaceAll(tt.name, " ", "-"))
			err := tt.d.Run(t.Context(), Classes(), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want one with %q", err, tt.want)
			}
		})
	}
	checkFiles(t, h.out, map[string]string{})
}

// TestDownloadCancelled cancels the download during the copy: it stops,
// the destination is released and a temporary file goes.
func TestDownloadCancelled(t *testing.T) {
	log := fakeTools(t)
	h := newHarness(t)
	point := h.dst(t, "point")
	a := h.dst(t, "point/a")
	writeFiles(t, a, map[string]string{"www-2026-11-19-d.txt": strings.Repeat("x", 1<<20)})
	cfg := h.config(t, "www.yml", cmdConfig("printf www", a, "  mount: {point: "+point+", umount: true}\n"))
	out := h.dst(t, "out")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d := Download{Config: cfg, Archive: "www-2026-11-19-d.txt", Filename: "-"}
	err := d.Run(ctx, Classes(), &cancelWriter{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want cancelled", err)
	}
	if m, u := countCalls(t, log, "mount "), countCalls(t, log, "umount "); m != 1 || u != 1 {
		t.Errorf("%d mounts and %d umounts, want one each", m, u)
	}

	checkFiles(t, out, map[string]string{})

	// Cancelled while writing a file: the temporary file goes.
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	r := ctxReader{ctx: ctx, r: &cancelReader{r: strings.NewReader("data"), cancel: cancel}}
	if err := writeFile(filepath.Join(out, "got"), false, r); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want cancelled", err)
	}
	checkFiles(t, out, map[string]string{})
}

// cancelReader cancels at its first read.
type cancelReader struct {
	r      *strings.Reader
	cancel context.CancelFunc
}

func (c *cancelReader) Read(p []byte) (int, error) {
	c.cancel()
	return c.r.Read(p)
}

// cancelWriter cancels at its first write.
type cancelWriter struct {
	cancel context.CancelFunc
}

func (c *cancelWriter) Write(p []byte) (int, error) {
	c.cancel()
	return len(p), nil
}
