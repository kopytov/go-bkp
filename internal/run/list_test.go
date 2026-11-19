package run

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listFiles makes files of the given sizes in dir; large ones are sparse.
func listFiles(t *testing.T, dir string, sizes map[string]int64) {
	t.Helper()
	for name, size := range sizes {
		f, err := os.Create(filepath.Join(dir, name)) //nolint:gosec // G304: a file in the test's directory
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// listHarness makes a config of profiles db, empty and www sharing one
// destination with archives, files that are not archives and a temporary
// file, and a config of one source in a destination of its own. It
// returns the paths of both configs.
func listHarness(t *testing.T) (multi, single string) {
	t.Helper()
	h := newHarness(t)
	shared := h.dst(t, "shared")
	listFiles(t, shared, map[string]int64{
		"db-2026-01-01-y.sql":     1<<20 - 1, // an archive from before compression
		"db-2026-11-01-m.sql.gz":  5000,
		"db-2026-11-17-d.sql.gz":  0,
		"db-2026-11-18-d.sql.gz":  1023,
		"db-2026-11-19-d.sql.gz":  1024,
		"www-2026-11-01-m.tar.gz": 2<<40 + 1,
		"www-2026-11-16-w.tar.gz": 7838315315, // 7.3G less a fraction of a byte
		"www-2026-11-19-d.tar.gz": 3446000,
		// Not archives.
		".www-2026-11-19-d.tar.gz.partial": 10,
		"www-2026-02-30-d.tar.gz":          10,
		"www-latest.tar.gz":                10,
		"wwwx-2026-11-19-d.tar.gz":         10,
		"notes.txt":                        10,
		"db-2026-11-19-x.sql.gz":           10,
	})
	multi = h.config(t, "multi.yml", fmt.Sprintf(`src:
  www: {class: cmd, cmd: [printf, w], suffix: tar}
  db: {class: cmd, cmd: [printf, d], suffix: sql}
  empty: {class: cmd, cmd: [printf, e], suffix: txt}
compress: {class: gzip}
dst: {class: local, dir: %s, plan: {d: 3}}
`, shared))
	own := h.dst(t, "own")
	listFiles(t, own, map[string]int64{
		"single-2026-11-18-d.txt": 2048,
		"single-2026-11-19-d.txt": 1536,
		"other-2026-11-19-d.txt":  1,
	})
	single = h.config(t, "single.yml", cmdConfig("printf s", own, ""))
	return multi, single
}

// TestList compares the output with the one Raku's bkp list gives on the
// same files, worked out from its code in testdata/list. Raku leaves out the
// file with the date that does not exist only since it is not an archive
// here; see doc/compat.md.
func TestList(t *testing.T) {
	t.Parallel()
	multi, single := listHarness(t)
	tests := []struct {
		name    string
		config  string
		bytes   bool
		profile string
		want    string
	}{
		{"multi", multi, false, "", "multi.txt"},
		{"multi bytes", multi, true, "", "multi-bytes.txt"},
		{"one of multi", multi, true, "www", "www-bytes.txt"},
		{"single", single, false, "", "single.txt"},
		{"single by name", single, false, "single", "single.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want, err := os.ReadFile(filepath.Join("testdata", "list", tt.want))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := List(t.Context(), &out, Classes(), tt.config, tt.bytes, tt.profile); err != nil {
				t.Fatal(err)
			}
			if out.String() != string(want) {
				t.Errorf("output:\n%s\nwant:\n%s", &out, want)
			}
		})
	}
}

func TestListEmptyProfile(t *testing.T) {
	t.Parallel()
	multi, _ := listHarness(t)
	var out bytes.Buffer
	if err := List(t.Context(), &out, Classes(), multi, false, "empty"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("output %q, want nothing", &out)
	}
}

func TestListErrors(t *testing.T) {
	t.Parallel()
	multi, _ := listHarness(t)
	h := newHarness(t)
	broken := h.config(t, "broken.yml", "src: {class: cmd}\n")
	missing := h.config(t, "missing.yml", cmdConfig("printf m", filepath.Join(h.dir, "gone"), ""))
	tests := []struct {
		name    string
		config  string
		profile string
		want    string
	}{
		{"unknown profile", multi, "nope", "profile nope not known in " + multi},
		{"broken config", broken, "", "broken.yml: "},
		{"missing config", filepath.Join(h.dir, "none.yml"), "", "none.yml: no such file"},
		{"missing dir", missing, "", "profile missing: dst: dir: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := List(t.Context(), &out, Classes(), tt.config, false, tt.profile)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want one with %q", err, tt.want)
			}
			if out.Len() != 0 {
				t.Errorf("output %q, want nothing", &out)
			}
		})
	}
}

// TestListMounts checks that the destination is mounted for the listing
// and unmounted after it, also when listing fails.
func TestListMounts(t *testing.T) {
	log := fakeTools(t)
	h := newHarness(t)
	point := h.dst(t, "point")
	a := h.dst(t, "point/a")
	listFiles(t, a, map[string]int64{"www-2026-11-19-d.txt": 3})
	cfg := h.config(t, "www.yml", cmdConfig("printf www", a, "  mount: {point: "+point+", umount: true}\n"))
	var out bytes.Buffer
	if err := List(t.Context(), &out, Classes(), cfg, true, ""); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "www-2026-11-19-d.txt 3\n" {
		t.Errorf("output %q", got)
	}
	if m, u := countCalls(t, log, "mount "), countCalls(t, log, "umount "); m != 1 || u != 1 {
		t.Errorf("%d mounts and %d umounts, want one each", m, u)
	}
	if _, err := os.Stat(filepath.Join(point, ".mounted")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("still mounted after the listing: %v", err)
	}

	// The directory is gone once mounted: listing fails, umount still runs.
	if err := os.Remove(filepath.Join(a, "www-2026-11-19-d.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if err := List(t.Context(), &out, Classes(), cfg, true, ""); err == nil {
		t.Error("List succeeded without the directory")
	}
	if u := countCalls(t, log, "umount "); u != 2 {
		t.Errorf("%d umounts, want 2", u)
	}

	t.Setenv("FAKE_UMOUNT_FAIL", "1")
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatal(err)
	}
	err := List(t.Context(), &out, Classes(), cfg, true, "")
	if err == nil || !strings.Contains(err.Error(), "profile www: dst: umount "+point) {
		t.Errorf("error %v, want the failed umount", err)
	}
}

func TestFormatSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		size int64
		want string
	}{
		{0, "0B"},
		{1, "1B"},
		{1023, "1023B"},
		{1024, "1.0K"},
		{1075, "1.0K"}, // 1.0498…
		{1076, "1.1K"}, // 1.0507…
		{1280, "1.2K"}, // exactly 1.25: to even
		{1331, "1.3K"}, // 1.2998…
		{1<<20 - 1, "1024.0K"},
		{1 << 20, "1.0M"},
		{1<<30 - 1, "1024.0M"},
		{1 << 30, "1.0G"},
		{1 << 40, "1.0T"},
		{1 << 50, "1024.0T"},
	}
	for _, tt := range tests {
		if got := formatSize(tt.size); got != tt.want {
			t.Errorf("formatSize(%d) = %q, want %q", tt.size, got, tt.want)
		}
	}
}
