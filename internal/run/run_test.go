package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/dst"
)

// today is the date archives are named after in the tests.
var today = time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local)

const pid = 4242

type harness struct {
	r              *Runner
	stdout, stderr *bytes.Buffer
	pauses         []time.Duration
	dir            string // configs and destinations live here
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, dir: t.TempDir()}
	h.r = &Runner{
		Classes: Classes(),
		Stdout:  h.stdout,
		Stderr:  h.stderr,
		PID:     pid,
		Now:     func() time.Time { return today },
		Pause: func(ctx context.Context, d time.Duration) error {
			h.pauses = append(h.pauses, d)
			return ctx.Err()
		},
	}
	return h
}

// config writes a config file and returns its path.
func (h *harness) config(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// dst makes a destination directory and returns its path.
func (h *harness) dst(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(h.dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// cmdConfig is a config of one source that runs script as src cmd into
// dir, with the extra top-level YAML extra.
func cmdConfig(script, dir, extra string) string {
	return fmt.Sprintf("src:\n  class: cmd\n  cmd: [sh, -c, %q]\n  suffix: txt\n"+
		"dst:\n  class: local\n  dir: %s\n  plan: {d: 3}\n%s", script, dir, extra)
}

func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, de := range des {
		data, err := os.ReadFile(filepath.Join(dir, de.Name())) //nolint:gosec // G304: a file the test run made
		if err != nil {
			t.Fatal(err)
		}
		out[de.Name()] = string(data)
	}
	return out
}

func checkFiles(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	got := files(t, dir)
	if !maps.Equal(got, want) {
		t.Errorf("%s holds %q, want %q", filepath.Base(dir), got, want)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func containsStderr(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("stderr lacks %q:\n%s", w, got)
		}
	}
}

func TestBackupAllGood(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.r.Log = true
	a, b := h.dst(t, "a"), h.dst(t, "b")
	cfgA := h.config(t, "www.yml", cmdConfig("printf www", a, ""))
	cfgB := h.config(t, "multi.yml", fmt.Sprintf(
		"src:\n  two: {class: cmd, cmd: [printf, two], suffix: txt}\n"+
			"  one: {class: cmd, cmd: [printf, one], suffix: txt}\n"+
			"dst: {class: local, dir: %s, plan: {d: 3}}\n", b))
	if !h.r.Backup(t.Context(), []string{cfgA, cfgB}) {
		t.Errorf("Backup failed; stderr:\n%s", h.stderr)
	}
	checkFiles(t, a, map[string]string{"www-2026-11-19-d.txt": "www"})
	checkFiles(t, b, map[string]string{"one-2026-11-19-d.txt": "one", "two-2026-11-19-d.txt": "two"})
	if h.stderr.Len() != 0 {
		t.Errorf("stderr:\n%s", h.stderr)
	}
	if len(h.pauses) != 0 {
		t.Errorf("paused %v", h.pauses)
	}

	stamp := today.Format(logTime) + fmt.Sprintf(" bkp: [%d] ", pid)
	var want []string
	for _, msg := range []string{
		"Got configs: " + cfgA,
		"Got configs: " + cfgB,
		"www.yml started",
		"Got profiles: www",
		"www.yml:www started [0]",
		"www.yml:www writing www-2026-11-19-d.txt",
		"www.yml:www finished [0]",
		"www.yml finished",
		"multi.yml started",
		"Got profiles: one",
		"Got profiles: two",
		"multi.yml:one started [0]",
		"multi.yml:one writing one-2026-11-19-d.txt",
		"multi.yml:one finished [0]",
		"multi.yml:two started [0]",
		"multi.yml:two writing two-2026-11-19-d.txt",
		"multi.yml:two finished [0]",
		"multi.yml finished",
	} {
		want = append(want, stamp+msg)
	}
	if got := strings.Split(strings.TrimSuffix(h.stdout.String(), "\n"), "\n"); !slices.Equal(got, want) {
		t.Errorf("log:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestLogFormat(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.r.Log = true
	h.r.Now = func() time.Time {
		return time.Date(2026, 11, 19, 3, 4, 5, 999_000_000, time.FixedZone("", 3*3600))
	}
	h.r.logf("x %d", 1)
	if got, want := h.stdout.String(), "2026-11-19T03:04:05+03:00 bkp: [4242] x 1\n"; got != want {
		t.Errorf("log line %q, want %q", got, want)
	}
	h.stdout.Reset()
	h.r.Now = func() time.Time { return time.Date(2026, 11, 19, 3, 4, 5, 0, time.UTC) }
	h.r.logf("x")
	if got, want := h.stdout.String(), "2026-11-19T03:04:05Z bkp: [4242] x\n"; got != want {
		t.Errorf("log line %q, want %q", got, want)
	}
}

func TestNoLogWithoutBKPLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.dst(t, "a")
	if !h.r.Backup(t.Context(), []string{h.config(t, "www.yml", cmdConfig("printf www; echo noise >&2", a, ""))}) {
		t.Fatalf("Backup failed; stderr:\n%s", h.stderr)
	}
	if h.stdout.Len() != 0 || h.stderr.Len() != 0 {
		t.Errorf("stdout %q, stderr %q, want both empty", h.stdout, h.stderr)
	}
}

func TestBackupStageStderrUnderLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.r.Log = true
	a := h.dst(t, "a")
	if !h.r.Backup(t.Context(), []string{h.config(t, "www.yml", cmdConfig("printf www; echo noise >&2", a, ""))}) {
		t.Fatalf("Backup failed; stderr:\n%s", h.stderr)
	}
	if got := h.stderr.String(); got != "noise\n" {
		t.Errorf("stderr %q, want the stage stderr", got)
	}
}

// TestBackupScriptFails covers the incident: a config whose src.script
// fails next to a working one.
func TestBackupScriptFails(t *testing.T) {
	t.Parallel()
	for _, script := range []string{"false", "false | cat"} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			a := h.dst(t, "a")
			broken := h.config(t, "mariadb.yaml", fmt.Sprintf(
				"src:\n  script: %q\n  template: |\n    {{ item }}: {class: cmd, cmd: [printf, x], suffix: txt}\n"+
					"dst: {class: local, dir: %s, plan: {d: 3}}\n", script, a))
			good := h.config(t, "www.yml", cmdConfig("printf www", a, ""))
			if h.r.Backup(t.Context(), []string{broken, good}) {
				t.Error("Backup succeeded with a config that does not load")
			}
			checkFiles(t, a, map[string]string{"www-2026-11-19-d.txt": "www"})
			containsStderr(t, h.stderr.String(), "mariadb.yaml loading failed - skipping\n", "exit status 1")
		})
	}
}

func TestBackupMountFails(t *testing.T) {
	fakeTools(t)
	h := newHarness(t)
	a := h.dst(t, "a")
	point := h.dst(t, "point")
	broken := h.config(t, "nas.yml", cmdConfig("printf nas", a,
		"  mount: {point: "+point+", cmd: [sh, -c, 'echo cannot mount >&2; exit 32']}\n"))
	good := h.config(t, "www.yml", cmdConfig("printf www", a, ""))
	if h.r.Backup(t.Context(), []string{broken, good}) {
		t.Error("Backup succeeded with a destination that does not mount")
	}
	checkFiles(t, a, map[string]string{"www-2026-11-19-d.txt": "www"})
	stderr := h.stderr.String()
	containsStderr(t, stderr, "nas.yml:nas failed [0]\n", "nas.yml:nas exception: dst: mount "+point,
		"cannot mount", "nas.yml:nas failed [9]\n", "BACKUP nas.yml:nas FAILED!\n")
	if n := strings.Count(stderr, "exception:"); n != attempts {
		t.Errorf("%d failed attempts, want %d", n, attempts)
	}
	var want []time.Duration
	for n := range attempts - 1 {
		want = append(want, time.Duration(n)*180*time.Second)
	}
	if !slices.Equal(h.pauses, want) {
		t.Errorf("pauses %v, want %v", h.pauses, want)
	}
}

func TestBackupStageFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.dst(t, "a")
	old := map[string]string{
		"www-2026-11-16-d.txt": "16", "www-2026-11-17-d.txt": "17", "www-2026-11-18-d.txt": "18",
	}
	writeFiles(t, a, old)
	cfg := h.config(t, "www.yml", cmdConfig("printf partial; echo dump died >&2; exit 3", a, ""))
	if h.r.Backup(t.Context(), []string{cfg}) {
		t.Error("Backup succeeded with a failing stage")
	}
	checkFiles(t, a, old)
	containsStderr(t, h.stderr.String(), "www.yml:www exception: src cmd: exit status 3; stderr: dump died",
		"BACKUP www.yml:www FAILED!")
}

func TestBackupSecondAttempt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.dst(t, "a")
	flag := filepath.Join(h.dir, "failed-once")
	script := fmt.Sprintf("if [ -e %[1]s ]; then printf ok; else touch %[1]s; printf half; exit 1; fi", flag)
	if !h.r.Backup(t.Context(), []string{h.config(t, "www.yml", cmdConfig(script, a, ""))}) {
		t.Errorf("Backup failed; stderr:\n%s", h.stderr)
	}
	checkFiles(t, a, map[string]string{"www-2026-11-19-d.txt": "ok"})
	stderr := h.stderr.String()
	containsStderr(t, stderr, "www.yml:www failed [0]\n")
	if strings.Contains(stderr, "FAILED!") {
		t.Errorf("stderr reports the profile failed:\n%s", stderr)
	}
	if want := []time.Duration{0}; !slices.Equal(h.pauses, want) {
		t.Errorf("pauses %v, want %v", h.pauses, want)
	}
}

func TestBackupSameDayReplaces(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.dst(t, "a")
	// An archive of today under an older suffix, before compression was
	// added, is replaced too.
	writeFiles(t, a, map[string]string{"www-2026-11-18-d.txt": "18", "www-2026-11-19-d.raw": "older suffix"})
	for _, data := range []string{"first", "second"} {
		cfg := h.config(t, "www.yml", strings.Replace(cmdConfig("printf "+data, a, ""), "suffix: txt", "suffix: raw.txt", 1))
		if !h.r.Backup(t.Context(), []string{cfg}) {
			t.Fatalf("Backup failed; stderr:\n%s", h.stderr)
		}
	}
	checkFiles(t, a, map[string]string{"www-2026-11-18-d.txt": "18", "www-2026-11-19-d.raw.txt": "second"})
}

func TestBackupRotates(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.r.Log = true
	a, b := h.dst(t, "a"), h.dst(t, "b")
	old := map[string]string{
		"www-2026-11-15-d.txt": "15", "www-2026-11-16-d.txt": "16",
		"www-2026-11-17-d.txt": "17", "www-2026-11-18-d.txt": "18", "notes": "keep",
	}
	writeFiles(t, a, old)
	writeFiles(t, b, old)
	cfgA := h.config(t, "a.yml", strings.Replace(cmdConfig("printf www", a, ""), "class: local", "class: local\n  prefix: www", 1))
	cfgB := h.config(t, "b.yml", strings.Replace(cmdConfig("printf www", b, "  no-rotate: true\n"), "class: local", "class: local\n  prefix: www", 1))
	if !h.r.Backup(t.Context(), []string{cfgA, cfgB}) {
		t.Fatalf("Backup failed; stderr:\n%s", h.stderr)
	}
	checkFiles(t, a, map[string]string{
		"www-2026-11-17-d.txt": "17", "www-2026-11-18-d.txt": "18", "www-2026-11-19-d.txt": "www", "notes": "keep",
	})
	wantB := map[string]string{"www-2026-11-19-d.txt": "www"}
	maps.Copy(wantB, old)
	checkFiles(t, b, wantB)
	log := h.stdout.String()
	for _, w := range []string{"a.yml:a deleting www-2026-11-15-d.txt\n", "a.yml:a deleting www-2026-11-16-d.txt\n"} {
		if !strings.Contains(log, w) {
			t.Errorf("log lacks %q:\n%s", w, log)
		}
	}
	if strings.Contains(log, "b.yml:b deleting") {
		t.Errorf("log shows deletes with no-rotate:\n%s", log)
	}
}

func TestBackupPermissions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.dst(t, "a")
	if !h.r.Backup(t.Context(), []string{h.config(t, "www.yml", cmdConfig("printf www", a, ""))}) {
		t.Fatalf("Backup failed; stderr:\n%s", h.stderr)
	}
	fi, err := os.Stat(filepath.Join(a, "www-2026-11-19-d.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("archive mode %o, want 600", perm)
	}
}

func TestBackupPost(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.dst(t, "a")
	ok := h.config(t, "ok.yml", cmdConfig("printf ok", a, "post: echo post ran | cat\n"))
	failing := h.config(t, "bad.yml", cmdConfig("printf bad", a, "post: echo hook broke >&2; false | cat\n"))
	after := h.config(t, "z.yml", cmdConfig("printf z", a, ""))
	if h.r.Backup(t.Context(), []string{ok, failing, after}) {
		t.Error("Backup succeeded with a failing post hook")
	}
	checkFiles(t, a, map[string]string{
		"ok-2026-11-19-d.txt": "ok", "bad-2026-11-19-d.txt": "bad", "z-2026-11-19-d.txt": "z",
	})
	if got := h.stdout.String(); got != "post ran\n" {
		t.Errorf("stdout %q, want the output of the post hook", got)
	}
	stderr := h.stderr.String()
	containsStderr(t, stderr, "hook broke\n", "bad.yml:bad post hook failed: exit status 1\n")
	if strings.Contains(stderr, "failed [") || strings.Contains(stderr, "FAILED!") {
		t.Errorf("a failing post hook retried or failed the profile:\n%s", stderr)
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

func countCalls(t *testing.T, log, prefix string) int {
	t.Helper()
	data, err := os.ReadFile(log) //nolint:gosec // G304: the log file of the test
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	n := 0
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

func TestBackupUmountsAfterFailedAttempts(t *testing.T) {
	log := fakeTools(t)
	h := newHarness(t)
	point := h.dst(t, "point")
	a := h.dst(t, "point/a")
	cfg := h.config(t, "www.yml", cmdConfig("exit 3", a, "  mount: {point: "+point+", umount: true}\n"))
	if h.r.Backup(t.Context(), []string{cfg}) {
		t.Error("Backup succeeded with a failing stage")
	}
	if n := countCalls(t, log, "mount "); n != attempts {
		t.Errorf("%d mounts, want %d", n, attempts)
	}
	if n := countCalls(t, log, "umount "); n != attempts {
		t.Errorf("%d umounts, want %d", n, attempts)
	}
	if _, err := os.Stat(filepath.Join(point, ".mounted")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("still mounted after the run: %v", err)
	}
}

func TestBackupUmountFails(t *testing.T) {
	fakeTools(t)
	t.Setenv("FAKE_UMOUNT_FAIL", "1")
	h := newHarness(t)
	point := h.dst(t, "point")
	a := h.dst(t, "point/a")
	cfg := h.config(t, "www.yml", cmdConfig("printf www", a, "  mount: {point: "+point+", umount: true}\npost: echo post ran\n"))
	if h.r.Backup(t.Context(), []string{cfg}) {
		t.Error("Backup succeeded with a failing umount")
	}
	checkFiles(t, a, map[string]string{"www-2026-11-19-d.txt": "www"})
	stderr := h.stderr.String()
	containsStderr(t, stderr, "www.yml:www cleanup failed: dst: umount "+point, "target is busy")
	if strings.Contains(stderr, "failed [") || strings.Contains(stderr, "FAILED!") {
		t.Errorf("a failing umount after a written archive retried or failed the profile:\n%s", stderr)
	}
	if got := h.stdout.String(); got != "post ran\n" {
		t.Errorf("stdout %q, want the post hook to run", got)
	}
}

// TestBackupCancelled cancels the run while the source is sending.
func TestBackupCancelled(t *testing.T) {
	log := fakeTools(t)
	h := newHarness(t)
	point := h.dst(t, "point")
	a := h.dst(t, "point/a")
	started := filepath.Join(h.dir, "started")
	pidFile := filepath.Join(h.dir, "child")
	// The source leaves a child behind that would keep writing.
	script := fmt.Sprintf("printf half; sleep 60 & echo $! > %s; touch %s; wait", pidFile, started)
	slow := h.config(t, "slow.yml", cmdConfig(script, a, "  mount: {point: "+point+", umount: true}\n"))
	next := h.config(t, "next.yml", cmdConfig("printf next", a, ""))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go cancelWhen(ctx, cancel, started)
	done := make(chan bool)
	go func() { done <- h.r.Backup(ctx, []string{slow, next}) }()
	select {
	case ok := <-done:
		if ok {
			t.Error("Backup succeeded after a cancel")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Backup did not return after a cancel")
	}
	checkFiles(t, a, map[string]string{})
	if n := countCalls(t, log, "umount "); n != 1 {
		t.Errorf("%d umounts, want 1", n)
	}
	if len(h.pauses) != 0 {
		t.Errorf("paused %v after a cancel", h.pauses)
	}
	stderr := h.stderr.String()
	containsStderr(t, stderr, "slow.yml:slow failed [0]\n")
	if strings.Contains(stderr, "failed [1]") {
		t.Errorf("retried after a cancel:\n%s", stderr)
	}
	if child := readPID(t, pidFile); !gone(child) {
		t.Errorf("child %d of the source still running", child)
	}
}

// gone waits a little for pid to die, as the SIGKILL to its group may
// reach it after the call that sent it returned, and reports whether it did.
func gone(pid int) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// processAlive reports whether pid runs; a zombie does not count, as its
// parent, the shell, is gone and init reaps it.
func processAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(data, ')')
	return i >= 0 && i+2 < len(data) && data[i+2] != 'Z'
}

func TestBackupUmountFailsAfterFailedAttempt(t *testing.T) {
	fakeTools(t)
	t.Setenv("FAKE_UMOUNT_FAIL", "1")
	h := newHarness(t)
	point := h.dst(t, "point")
	a := h.dst(t, "point/a")
	cfg := h.config(t, "www.yml", cmdConfig("echo dump died >&2; exit 3", a, "  mount: {point: "+point+", umount: true}\n"))
	if h.r.Backup(t.Context(), []string{cfg}) {
		t.Error("Backup succeeded with a failing stage")
	}
	stderr := h.stderr.String()
	containsStderr(t, stderr, "www.yml:www exception: src cmd: exit status 3; stderr: dump died",
		"dst: umount "+point, "target is busy", "www.yml:www failed [9]\n", "BACKUP www.yml:www FAILED!\n")
	if strings.Contains(stderr, "cleanup failed") {
		t.Errorf("a failed attempt reported as a cleanup failure:\n%s", stderr)
	}
}

func TestDefaultPauseStopsOnCancel(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if err := r.pause(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("pause error %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("pause took %v after a cancel", d)
	}
	if err := r.pause(t.Context(), time.Millisecond); err != nil {
		t.Errorf("pause: %v", err)
	}
}

// cancelWhen cancels ctx once path exists.
func cancelWhen(ctx context.Context, cancel context.CancelFunc, path string) {
	for {
		if _, err := os.Stat(path); err == nil {
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: a file the test made
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(string(data), &pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestBackupPostCancelled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.dst(t, "a")
	started := filepath.Join(h.dir, "started")
	pidFile := filepath.Join(h.dir, "child")
	post := fmt.Sprintf("post: sleep 60 & echo $! > %s; touch %s; wait\n", pidFile, started)
	cfg := h.config(t, "www.yml", cmdConfig("printf www", a, post))
	next := h.config(t, "next.yml", cmdConfig("printf next", a, ""))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go cancelWhen(ctx, cancel, started)
	done := make(chan bool)
	go func() { done <- h.r.Backup(ctx, []string{cfg, next}) }()
	select {
	case ok := <-done:
		if ok {
			t.Error("Backup succeeded after a cancel")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Backup did not return after a cancel")
	}
	checkFiles(t, a, map[string]string{"www-2026-11-19-d.txt": "www"})
	containsStderr(t, h.stderr.String(), "www.yml:www post hook failed: signal: killed")
	if child := readPID(t, pidFile); !gone(child) {
		t.Errorf("child %d of the post hook still running", child)
	}
}

func TestBackupRotateFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.dst(t, "a")
	// A directory with a file in it cannot be deleted as an archive.
	stuck := filepath.Join(a, "www-2026-11-15-d.txt")
	if err := os.Mkdir(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, stuck, map[string]string{"inside": "x"})
	writeFiles(t, a, map[string]string{
		"www-2026-11-16-d.txt": "16", "www-2026-11-17-d.txt": "17", "www-2026-11-18-d.txt": "18",
	})
	cfg := h.config(t, "www.yml", cmdConfig("printf www", a, "post: echo post ran\n"))
	if h.r.Backup(t.Context(), []string{cfg}) {
		t.Error("Backup succeeded with a failing rotation")
	}
	if err := os.RemoveAll(stuck); err != nil {
		t.Fatal(err)
	}
	// The next archive due for deletion is deleted all the same.
	checkFiles(t, a, map[string]string{
		"www-2026-11-17-d.txt": "17", "www-2026-11-18-d.txt": "18", "www-2026-11-19-d.txt": "www",
	})
	stderr := h.stderr.String()
	containsStderr(t, stderr, "www.yml:www rotate failed: dst: delete", "www-2026-11-15-d.txt")
	if strings.Contains(stderr, "failed [") || strings.Contains(stderr, "FAILED!") {
		t.Errorf("a failing rotation after a written archive retried or failed the profile:\n%s", stderr)
	}
	if got := h.stdout.String(); got != "post ran\n" {
		t.Errorf("stdout %q, want the post hook to run", got)
	}
}

// hookDst is dst class local with Delete, Umount and Write replaceable by
// the test.
type hookDst struct {
	*dst.Local
	delete func(ctx context.Context, name string) error
	umount func(ctx context.Context) error
	// write gets the Local to write with.
	write func(l *dst.Local, ctx context.Context, name string, send func(*os.File) error) error
}

func (d *hookDst) Delete(ctx context.Context, name string) error {
	if d.delete != nil {
		return d.delete(ctx, name)
	}
	return d.Local.Delete(ctx, name)
}

func (d *hookDst) Umount(ctx context.Context) error {
	if d.umount != nil {
		return d.umount(ctx)
	}
	return d.Local.Umount(ctx)
}

func (d *hookDst) Write(ctx context.Context, name string, send func(*os.File) error) error {
	if d.write != nil {
		return d.write(d.Local, ctx, name, send)
	}
	return d.Local.Write(ctx, name, send)
}

// useHooks makes dst class local of h a hookDst with the hooks of tmpl.
func (h *harness) useHooks(tmpl hookDst) {
	h.r.Classes.Dst = maps.Clone(h.r.Classes.Dst)
	h.r.Classes.Dst["local"] = func(n *yaml.Node) (config.Dest, error) {
		l, err := dst.NewLocal(n)
		d := tmpl
		d.Local = l
		return &d, err
	}
}

func TestBackupFailuresAfterWriteAndCancel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h.useHooks(hookDst{
		delete: func(context.Context, string) error {
			cancel() // a signal arrives during rotation
			return errors.New("delete refused")
		},
		umount: func(context.Context) error { return errors.New("umount refused") },
	})
	a := h.dst(t, "a")
	old := map[string]string{
		"www-2026-11-15-d.txt": "15", "www-2026-11-16-d.txt": "16",
		"www-2026-11-17-d.txt": "17", "www-2026-11-18-d.txt": "18",
	}
	writeFiles(t, a, old)
	cfg := h.config(t, "www.yml", cmdConfig("printf www", a, "post: echo post ran\n"))
	next := h.config(t, "next.yml", cmdConfig("printf next", a, ""))
	if h.r.Backup(ctx, []string{cfg, next}) {
		t.Error("Backup succeeded")
	}
	want := map[string]string{"www-2026-11-19-d.txt": "www"}
	maps.Copy(want, old)
	checkFiles(t, a, want)
	if h.stdout.Len() != 0 {
		t.Errorf("post ran after a cancel: stdout %q", h.stdout)
	}
	stderr := h.stderr.String()
	wantLines := []string{
		"www.yml:www rotate failed: dst: delete refused",
		"www.yml:www rotate failed: dst: delete refused",
		"www.yml:www cleanup failed: dst: umount refused",
	}
	if got := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n"); !slices.Equal(got, wantLines) {
		t.Errorf("stderr:\n%s\nwant:\n%s", stderr, strings.Join(wantLines, "\n"))
	}
}

func TestBackupInPlaceError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.useHooks(hookDst{
		write: func(l *dst.Local, ctx context.Context, name string, send func(*os.File) error) error {
			if err := l.Write(ctx, name, send); err != nil {
				return err
			}
			return fmt.Errorf("%w, but sync: boom", config.ErrInPlace)
		},
	})
	a := h.dst(t, "a")
	cfg := h.config(t, "www.yml", cmdConfig("printf www", a, "post: echo post ran\n"))
	if h.r.Backup(t.Context(), []string{cfg}) {
		t.Error("Backup succeeded with a failed sync")
	}
	checkFiles(t, a, map[string]string{"www-2026-11-19-d.txt": "www"})
	stderr := h.stderr.String()
	containsStderr(t, stderr, "www.yml:www dst: archive in place, but sync: boom\n")
	if strings.Contains(stderr, "failed [") || strings.Contains(stderr, "FAILED!") {
		t.Errorf("an archive in place was sent again or failed the profile:\n%s", stderr)
	}
	if got := h.stdout.String(); got != "post ran\n" {
		t.Errorf("stdout %q, want the post hook to run", got)
	}
}
