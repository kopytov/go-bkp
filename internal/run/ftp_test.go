package run

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kopytov/go-bkp/internal/dst/ftptest"
)

// ftpConfig is a config of one source that runs script as src cmd into
// path on s, keeping 3 daily archives.
func ftpConfig(script string, s *ftptest.Server, path string) string {
	return fmt.Sprintf("src:\n  class: cmd\n  cmd: [sh, -c, %q]\n  suffix: txt\n"+
		"dst: {class: ftp, %s, plan: {d: 3}}\n", script, s.Attrs(path))
}

// TestBackupFTP runs the full cycle against an FTP server that does not
// rename onto an existing file: the archive is sent and the old ones
// rotated, a second run the same day replaces the archive,
// and the archives are listed and downloaded.
func TestBackupFTP(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{NoOverwrite: true})
	h := newHarness(t)
	// Older archives to rotate; making the path is tested in dst.
	www := filepath.Join(s.Root, "backups", "www")
	if err := os.MkdirAll(www, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, www, map[string]string{
		"www-2026-11-15-d.txt": "15", "www-2026-11-16-d.txt": "16",
		"www-2026-11-17-d.txt": "17", "www-2026-11-18-d.txt": "18", "notes": "keep",
	})
	for _, data := range []string{"first", "second"} {
		cfg := h.config(t, "www.yml", ftpConfig("printf "+data, s, "/backups/www"))
		if !h.r.Backup(t.Context(), []string{cfg}) {
			t.Fatalf("Backup %s failed; stderr:\n%s", data, h.stderr)
		}
		checkFiles(t, www, map[string]string{
			"www-2026-11-17-d.txt": "17", "www-2026-11-18-d.txt": "18",
			"www-2026-11-19-d.txt": data, "notes": "keep",
		})
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr:\n%s", h.stderr)
	}

	cfg := filepath.Join(h.dir, "www.yml")
	var out bytes.Buffer
	if err := List(t.Context(), &out, Classes(), cfg, true, ""); err != nil {
		t.Fatalf("List: %v", err)
	}
	want := "www-2026-11-17-d.txt 2\nwww-2026-11-18-d.txt 2\nwww-2026-11-19-d.txt 6\n"
	if out.String() != want {
		t.Errorf("List output:\n%s\nwant:\n%s", &out, want)
	}

	got := h.dst(t, "got")
	d := Download{Config: cfg, Archive: "www-2026-11-19-d.txt", Filename: filepath.Join(got, "www")}
	if err := d.Run(t.Context(), Classes(), &out); err != nil {
		t.Fatalf("Download: %v", err)
	}
	checkFiles(t, got, map[string]string{"www": "second"})
}

// TestBackupFTPStageFails checks that a failed stage leaves nothing on the
// server, in any attempt, and the password stays out of stderr.
func TestBackupFTPStageFails(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{})
	h := newHarness(t)
	cfg := h.config(t, "www.yml", ftpConfig("printf half; exit 3", s, ""))
	if h.r.Backup(t.Context(), []string{cfg}) {
		t.Fatal("Backup succeeded")
	}
	checkFiles(t, s.Root, map[string]string{})
	containsStderr(t, h.stderr.String(), "BACKUP www.yml:www FAILED!")
	if strings.Contains(h.stderr.String(), ftptest.Password) {
		t.Errorf("stderr shows the password:\n%s", h.stderr)
	}
}

// TestDownloadFTPTruncated checks that a download the server breaks off
// fails and leaves no file.
func TestDownloadFTPTruncated(t *testing.T) {
	t.Parallel()
	s := ftptest.New(t, ftptest.Faults{TruncateAfter: 3})
	h := newHarness(t)
	writeFiles(t, s.Root, map[string]string{"www-2026-11-19-d.txt": "archive"})
	cfg := h.config(t, "www.yml", ftpConfig("printf www", s, ""))
	got := h.dst(t, "got")
	var out bytes.Buffer
	for _, filename := range []string{filepath.Join(got, "www"), "-"} {
		d := Download{Config: cfg, Archive: "www-2026-11-19-d.txt", Filename: filename}
		if err := d.Run(t.Context(), Classes(), &out); err == nil || !strings.Contains(err.Error(), "426") {
			t.Errorf("Download to %s error %v, want 426", filename, err)
		}
	}
	checkFiles(t, got, map[string]string{})
}
