package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helperEnv makes the test binary run as bkp, with no pause between
// attempts.
const helperEnv = "BKP_TEST_AS_BKP"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) != "" {
		noPause := func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
		os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr, noPause))
	}
	os.Exit(m.Run())
}

// bkp returns a command running the test binary as bkp with args, with the
// fake mountpoint first in PATH.
func bkp(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := filepath.Abs(filepath.Join("testdata", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, args...)
	// A binary built with -race sleeps a second at exit unless told not to.
	cmd.Env = append(os.Environ(), helperEnv+"=1", "BKP_LOG=", "GORACE=atexit_sleep_ms=0",
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cmd
}

type result struct {
	code           int
	stdout, stderr string
}

func runBkp(t *testing.T, cmd *exec.Cmd) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return result{code: cmd.ProcessState.ExitCode(), stdout: stdout.String(), stderr: stderr.String()}
}

func writeConfig(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// cmdConfig is a config of one source that runs script into dir.
func cmdConfig(script, dir, extra string) string {
	return fmt.Sprintf("src:\n  class: cmd\n  cmd: [sh, -c, %q]\n  suffix: txt\n"+
		"dst:\n  class: local\n  dir: %s\n  plan: {d: 3}\n%s", script, dir, extra)
}

// date matches the date in an archive name, which bkp takes from its own
// clock.
var date = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

func archives(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, date.ReplaceAllString(de.Name(), "DATE"))
	}
	return names
}

func checkArchives(t *testing.T, dir string, want ...string) {
	t.Helper()
	got := archives(t, dir)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("%s holds %q, want %q", dir, got, want)
	}
}

func TestNoArguments(t *testing.T) {
	t.Parallel()
	res := runBkp(t, bkp(t))
	if res.code != 2 || res.stderr != usage || res.stdout != "" {
		t.Errorf("bkp without arguments: %+v", res)
	}
}

func TestAllGood(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := writeConfig(t, dir, "a.yml", cmdConfig("printf a", dir, ""))
	b := writeConfig(t, dir, "b.yml", cmdConfig("printf b", dir, "post: echo post ran\n"))
	res := runBkp(t, bkp(t, a, b))
	if res.code != 0 || res.stderr != "" || res.stdout != "post ran\n" {
		t.Errorf("bkp: %+v", res)
	}
	checkArchives(t, dir, "a-DATE-d.txt", "a.yml", "b-DATE-d.txt", "b.yml")

	cmd := bkp(t, a)
	cmd.Env = append(cmd.Env, "BKP_LOG=1")
	res = runBkp(t, cmd)
	if res.code != 0 || res.stderr != "" {
		t.Errorf("bkp with BKP_LOG: %+v", res)
	}
	if !strings.Contains(res.stdout, fmt.Sprintf(" bkp: [%d] a.yml:a finished [0]\n", cmd.Process.Pid)) {
		t.Errorf("progress log lacks the finished line:\n%s", res.stdout)
	}
}

// TestBrokenScript is the incident: a config whose src.script fails next
// to a working one.
func TestBrokenScript(t *testing.T) {
	t.Parallel()
	for _, script := range []string{"false", "false | cat"} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			broken := writeConfig(t, dir, "mariadb.yaml", fmt.Sprintf(
				"src:\n  script: %q\n  template: |\n    {{ item }}: {class: cmd, cmd: [printf, x], suffix: txt}\n"+
					"dst: {class: local, dir: %s, plan: {d: 3}}\n", script, dir))
			good := writeConfig(t, dir, "www.yml", cmdConfig("printf www", dir, ""))
			res := runBkp(t, bkp(t, broken, good))
			if res.code != 1 {
				t.Errorf("exit status %d, want 1; stderr:\n%s", res.code, res.stderr)
			}
			checkArchives(t, dir, "mariadb.yaml", "www-DATE-d.txt", "www.yml")
			for _, want := range []string{"mariadb.yaml loading failed - skipping\n", "exit status 1"} {
				if !strings.Contains(res.stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, res.stderr)
				}
			}
			if res.stdout != "" {
				t.Errorf("stdout %q, want nothing", res.stdout)
			}
		})
	}
}

func TestDstDoesNotMount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	point := t.TempDir()
	nas := writeConfig(t, dir, "nas.yml", cmdConfig("printf nas", dir, "  mount: {point: "+point+", cmd: [\"false\"]}\n"))
	good := writeConfig(t, dir, "www.yml", cmdConfig("printf www", dir, ""))
	res := runBkp(t, bkp(t, nas, good))
	if res.code != 1 {
		t.Errorf("exit status %d, want 1; stderr:\n%s", res.code, res.stderr)
	}
	checkArchives(t, dir, "nas.yml", "www-DATE-d.txt", "www.yml")
	if !strings.Contains(res.stderr, "BACKUP nas.yml:nas FAILED!\n") {
		t.Errorf("stderr lacks the failed profile:\n%s", res.stderr)
	}
}

func TestSignalDuringSend(t *testing.T) {
	t.Parallel()
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			started := filepath.Join(dir, "started")
			pidFile := filepath.Join(dir, "child")
			script := fmt.Sprintf("printf half; sleep 60 & echo $! > %s; touch %s; wait", pidFile, started)
			slow := writeConfig(t, dir, "slow.yml", cmdConfig(script, dir, ""))
			next := writeConfig(t, dir, "next.yml", cmdConfig("printf next", dir, ""))
			cmd := bkp(t, slow, next)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, started)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				var exit *exec.ExitError
				if err != nil && !errors.As(err, &exit) {
					t.Fatal(err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("bkp did not exit after the signal")
			}
			if code, want := cmd.ProcessState.ExitCode(), 128+int(sig); code != want {
				t.Errorf("exit status %d, want %d; stderr:\n%s", code, want, &stderr)
			}
			if err := os.Remove(started); err != nil {
				t.Fatal(err)
			}
			pid := readPID(t, pidFile)
			if err := os.Remove(pidFile); err != nil {
				t.Fatal(err)
			}
			checkArchives(t, dir, "next.yml", "slow.yml")
			if !gone(pid) {
				t.Errorf("child %d of the source still running", pid)
			}
			for _, want := range []string{"slow.yml:slow failed [0]\n", "bkp: stopped by " + signalName(sig) + "\n"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr lacks %q:\n%s", want, &stderr)
				}
			}
			if strings.Contains(stderr.String(), "failed [1]") {
				t.Errorf("retried after the signal:\n%s", &stderr)
			}
		})
	}
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", path)
		}
		time.Sleep(10 * time.Millisecond)
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

// gone waits a little for pid to die, as the kill of its group may reach
// it after bkp exited, and reports whether it did.
func gone(pid int) bool {
	for range 100 {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// processAlive reports whether pid runs and is not a zombie.
func processAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(data, ')')
	return i >= 0 && i+2 < len(data) && data[i+2] != 'Z'
}

func TestUsageError(t *testing.T) {
	t.Parallel()
	res := runBkp(t, bkp(t, "list", "a.yml", "b.yml"))
	if res.code != 2 || res.stderr != "bkp: list takes <config>\n"+usage || res.stdout != "" {
		t.Errorf("bkp list with two configs: %+v", res)
	}
}

func TestHelp(t *testing.T) {
	t.Parallel()
	res := runBkp(t, bkp(t, "--help"))
	if res.code != 0 || res.stdout != usage || res.stderr != "" {
		t.Errorf("bkp --help: %+v", res)
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()
	res := runBkp(t, bkp(t, "--version"))
	if res.code != 0 || res.stdout != "dev\n" || res.stderr != "" {
		t.Errorf("bkp --version: %+v", res)
	}
}

func TestListProfiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	multi := writeConfig(t, dir, "multi.yml", fmt.Sprintf(
		"src:\n  www: {class: cmd, cmd: [printf, w], suffix: txt}\n  db: {class: cmd, cmd: [printf, d], suffix: txt}\n"+
			"dst: {class: local, dir: %s, plan: {d: 3}}\n", dir))
	single := writeConfig(t, dir, "single.yml", cmdConfig("printf s", dir, ""))
	for _, tt := range []struct{ config, want string }{
		{multi, "db\nwww\n"},
		{single, "single\n"},
	} {
		res := runBkp(t, bkp(t, "list-profiles", tt.config))
		if res.code != 0 || res.stdout != tt.want || res.stderr != "" {
			t.Errorf("bkp list-profiles %s: %+v", tt.config, res)
		}
	}

	res := runBkp(t, bkp(t, "list-profiles", filepath.Join(dir, "missing.yml")))
	if res.code != 1 || res.stdout != "" || !strings.Contains(res.stderr, "missing.yml: no such file") {
		t.Errorf("bkp list-profiles of a missing config: %+v", res)
	}
}
