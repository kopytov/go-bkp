package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLoadREADMEScript loads the README example that lists MySQL databases,
// with a fake mysql first in PATH.
func TestLoadREADMEScript(t *testing.T) {
	bin, err := filepath.Abs(filepath.Join("testdata", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	mysql := func(name string) summary {
		return summary{name: name, src: "mysql", dst: "local", prefix: name, suffix: "mysql"}
	}
	checkLoad(t, filepath.Join("testdata", "readme-src-script.yml"),
		[]summary{mysql("mysql-shop"), mysql("mysql-wordpress")}, "")
}

func TestLoadScript(t *testing.T) {
	t.Parallel()
	tmpl := "  template: |\n    db-{{ item }}:\n      class: tar\n      files: {{item}}\n"
	script := func(s string) string { return "src:\n  script: |\n    " + s + "\n" + tmpl + dstLocal }
	tests := []struct {
		name    string
		text    string
		want    []summary
		wantErr string
	}{
		{name: "success", text: script(`printf 'b\na.1\nc_-\n'`), want: []summary{
			tar("db-a.1", "db-a.1"), tar("db-b", "db-b"), tar("db-c_-", "db-c_-"),
		}},
		{name: "no final line break", text: script(`printf b`), want: []summary{tar("db-b", "db-b")}},
		{
			name: "template without a final line break",
			text: "src:\n  script: printf 'a\\nb\\n'\n  template: '{{ item }}: {class: tar}'\n" + dstLocal,
			want: []summary{tar("a", "a"), tar("b", "b")},
		},
		{name: "non-zero exit", text: script(`echo oops >&2; exit 3`), wantErr: "src: script: exit status 3; stderr: oops"},
		{name: "pipefail", text: script(`false | cat`), wantErr: "src: script: exit status 1"},
		{name: "no output", text: script(`true`), wantErr: "src: script: printed nothing"},
		{name: "blank output", text: script(`echo`), wantErr: "src: script: printed nothing"},
		{name: "space in a line", text: script(`printf 'a\nb c\n'`), wantErr: `output line 2 "b c": only A-Z a-z 0-9 . _ - are allowed`},
		{name: "YAML in a line", text: script(`echo 'x: {class: cmd}'`), wantErr: `output line 1 "x: {class: cmd}"`},
		{name: "empty line", text: script(`printf 'a\n\nb\n'`), wantErr: `output line 2 ""`},
		{name: "slash in a line", text: script(`echo a/b`), wantErr: `output line 1 "a/b"`},
		{name: "same item twice", text: script(`printf 'a\na\n'`), wantErr: `key "db-a" given twice`},
		{name: "no template", text: "src:\n  script: echo a\n" + dstLocal, wantErr: "template is required"},
		{name: "template a list", text: "src:\n  script: echo a\n  template: [a]\n" + dstLocal, wantErr: "template: want a string, got a list"},
		{name: "script without a value", text: "src:\n  script:\n" + tmpl + dstLocal, wantErr: "script: want a string"},
		{name: "background process", text: script(`echo a; sleep 30 &`), wantErr: "left a background process holding its output open"},
		{name: "too much output", text: script(`head -c 2000000 /dev/zero | tr "\\0" a`), wantErr: "printed more than 1048576 bytes"},
		{name: "script a bool", text: "src:\n  script: true\n" + tmpl + dstLocal, wantErr: `script: want a string, got "true"`},
		{name: "script a list", text: "src:\n  script: [echo, a]\n" + tmpl + dstLocal, wantErr: "script: want a string, got a list"},
		{name: "other key", text: "src:\n  script: echo a\n  class: tar\n" + tmpl + dstLocal, wantErr: `unknown key "class" beside script`},
		{
			name: "rendered YAML not a mapping",
			text: "src:\n  script: echo a\n  template: '- {{ item }}'\n" + dstLocal, wantErr: "want a mapping of sources",
		},
		{
			name:    "rendered source unknown class",
			text:    "src:\n  script: echo a\n  template: '{{ item }}: {class: lvm}'\n" + dstLocal,
			wantErr: `profile a: src: line 1: unknown class "lvm"`,
		},
		{
			name: "prefix with a script", text: script(`echo a`) + "  prefix: x\n",
			wantErr: "dst.prefix is not allowed with several sources",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checkLoad(t, writeConfig(t, "x.yml", tt.text), tt.want, tt.wantErr)
		})
	}
}

func TestTailBuffer(t *testing.T) {
	t.Parallel()
	b := &tailBuffer{max: 5}
	if got := b.String(); got != "(empty)" {
		t.Errorf("empty: %q", got)
	}
	for _, s := range []string{"ab", "cd\n"} {
		if _, err := b.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.String(); got != "abcd" {
		t.Errorf("short: %q", got)
	}
	if _, err := b.Write([]byte("xyzабв")); err != nil {
		t.Fatal(err)
	}
	// The last 5 bytes start inside "а"; the cut moves to "б".
	if got := b.String(); got != "…бв" {
		t.Errorf("cut: %q", got)
	}
	long := &tailBuffer{max: 5}
	if _, err := long.Write([]byte("xyzабв\n")); err != nil {
		t.Fatal(err)
	}
	if got := long.String(); got != "…бв" {
		t.Errorf("cut in String: %q", got)
	}
}

// TestLoadScriptKillsBackground checks that a process the script leaves in
// the background, not holding its output, is killed once the script exits.
func TestLoadScriptKillsBackground(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	path := writeConfig(t, "x.yml", "src:\n  script: |\n    sleep 30 >/dev/null 2>&1 &\n    echo $! > "+pidFile+
		"\n    echo a\n  template: '{{ item }}: {class: tar}'\n"+dstLocal)
	if _, err := Load(t.Context(), path, testClasses()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(pidFile) //nolint:gosec // G304: a file in the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// The killed process is reaped by init, not by this test; until then
	// it is a zombie that signal 0 still finds.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck // best-effort cleanup of a failed test
			t.Fatalf("background process %d still exists: kill(0) = %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLoadScriptCancel checks that a cancelled load kills the script and
// what it started, and returns promptly.
func TestLoadScriptCancel(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "x.yml", "src:\n  script: sleep 30 | cat\n  template: '{{ item }}: {class: tar}'\n"+dstLocal)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Load(ctx, path, testClasses())
	if err == nil {
		t.Fatal("load did not fail")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("load took %v after the cancel", d)
	}
}
