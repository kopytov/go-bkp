package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test binary run as "<binary> helper <token> <mode> <args>..." plays a
// stage. The token marks the processes of one test, so that the test can
// look for any left behind.
func TestMain(m *testing.M) {
	if len(os.Args) > 3 && os.Args[1] == "helper" {
		os.Exit(helper(os.Args[2], os.Args[3], os.Args[4:]))
	}
	// Under -race a process sleeps a second on exit, every stage included.
	if err := os.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func helper(token, mode string, args []string) int {
	arg := func(i int) int {
		n, err := strconv.Atoi(args[i])
		if err != nil {
			panic(err)
		}
		return n
	}
	switch mode {
	case "gen": // gen <bytes>: write the test pattern
		if _, err := os.Stdout.Write(pattern(arg(0))); err != nil {
			return 10
		}
	case "flip": // invert every byte from stdin to stdout
		r, w := bufio.NewReader(os.Stdin), bufio.NewWriter(os.Stdout)
		for {
			b, err := r.ReadByte()
			if err != nil {
				break
			}
			if err := w.WriteByte(^b); err != nil {
				return 10
			}
		}
		if err := w.Flush(); err != nil {
			return 10
		}
	case "cat": // cat [status]: copy stdin to stdout, exit with status
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			return 10
		}
		if len(args) > 0 {
			return arg(0)
		}
	case "exit": // exit <status>
		return arg(0)
	case "stderr": // stderr <lines> <status>: numbered lines to stderr
		for i := range arg(0) {
			fmt.Fprintf(os.Stderr, "line %04d\n", i+1)
		}
		return arg(1)
	case "kill": // die of SIGKILL
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			return 10
		}
		time.Sleep(time.Hour)
	case "hang": // ignore SIGTERM and never exit
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Hour)
	case "spawn": // spawn exit|hang: start a hanging child, then exit or hang
		exe, err := os.Executable()
		if err != nil {
			return 10
		}
		child := exec.Command(exe, "helper", token, "hang") //nolint:gosec // G702: the test binary itself
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			return 10
		}
		if args[0] == "hang" {
			signal.Ignore(syscall.SIGTERM)
			time.Sleep(time.Hour)
		}
	case "escape": // escape <pidfile>: leave a child in a session of its own holding stderr
		exe, err := os.Executable()
		if err != nil {
			return 10
		}
		child := exec.Command(exe, "helper", token, "hang") //nolint:gosec // G702: the test binary itself
		child.Stderr = os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			return 10
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil { //nolint:gosec // G703: a path in the test's own temp dir
			return 10
		}
	case "fd3": // copy descriptor 3 to stdout
		if _, err := io.Copy(os.Stdout, os.NewFile(3, "fd3")); err != nil {
			return 10
		}
	default:
		panic("unknown helper mode " + mode)
	}
	return 0
}

// pattern returns n bytes that do not repeat at the pipe buffer size.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func flipped(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = ^c
	}
	return out
}

// big is larger than a pipe buffer many times over.
const big = 8 << 20

// tester builds the stages of one test and checks what they leave behind.
type tester struct {
	t     *testing.T
	exe   string
	token string
}

func newTester(t *testing.T) *tester {
	t.Helper()
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tt := &tester{t: t, exe: exe, token: "token=" + t.Name()}
	t.Cleanup(tt.checkNoneLeft)
	return tt
}

func (tt *tester) stage(name, mode string, args ...string) Stage {
	return Stage{Name: name, Argv: append([]string{tt.exe, "helper", tt.token, mode}, args...)}
}

// run starts s, reads its output to the end and waits for it, failing the
// test if that takes too long.
func (tt *tester) run(ctx context.Context, s Spec) ([]byte, error) {
	tt.t.Helper()
	p, err := Start(ctx, s)
	if err != nil {
		tt.t.Fatalf("Start: %v", err)
	}
	type res struct {
		out     []byte
		readErr error
		err     error
	}
	done := make(chan res, 1)
	go func() {
		var r res
		if o := p.Output(); o != nil {
			r.out, r.readErr = io.ReadAll(o)
			if err := o.Close(); err != nil && r.readErr == nil {
				r.readErr = err
			}
		}
		r.err = p.Wait()
		done <- r
	}()
	select {
	case r := <-done:
		if r.readErr != nil {
			tt.t.Fatalf("read output: %v", r.readErr)
		}
		return r.out, r.err
	case <-time.After(30 * time.Second):
		tt.t.Fatal("pipeline did not finish")
		return nil, nil
	}
}

// checkNoneLeft fails the test if a process of it is still alive; a
// zombie is dead and waits only for init to reap it.
func (tt *tester) checkNoneLeft() {
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive := tt.alive()
		if len(alive) == 0 {
			return
		}
		if time.Now().After(deadline) {
			tt.t.Errorf("processes left behind: %v", alive)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (tt *tester) alive() []int {
	paths, err := filepath.Glob("/proc/[0-9]*/cmdline")
	if err != nil {
		tt.t.Fatal(err)
	}
	var pids []int
	for _, path := range paths {
		cmdline, err := os.ReadFile(path) //nolint:gosec // G304: a file under /proc
		if err != nil || !bytes.Contains(cmdline, []byte("\x00"+tt.token+"\x00")) {
			continue
		}
		dir := filepath.Dir(path)
		stat, err := os.ReadFile(filepath.Join(dir, "stat")) //nolint:gosec // G304: a file under /proc
		if err != nil {
			continue
		}
		// The state follows the parenthesised command name.
		if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
			continue
		}
		pid, err := strconv.Atoi(filepath.Base(dir))
		if err != nil {
			tt.t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func TestChain(t *testing.T) {
	tt := newTester(t)
	out, err := tt.run(t.Context(), Spec{Stages: []Stage{
		tt.stage("gen", "gen", strconv.Itoa(big)),
		tt.stage("flip", "flip"),
		tt.stage("cat", "cat"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, flipped(pattern(big))) {
		t.Errorf("output of %d bytes differs from the input of %d flipped", len(out), big)
	}
}

func TestStageFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		stages func(*tester) []Stage
		want   string
	}{
		{"first", func(tt *tester) []Stage {
			return []Stage{tt.stage("one", "exit", "3"), tt.stage("two", "cat"), tt.stage("three", "cat")}
		}, "one: exit status 3"},
		{"middle", func(tt *tester) []Stage {
			return []Stage{tt.stage("one", "gen", strconv.Itoa(big)), tt.stage("two", "exit", "4"), tt.stage("three", "cat")}
		}, "two: exit status 4"},
		{"last", func(tt *tester) []Stage {
			return []Stage{tt.stage("one", "gen", strconv.Itoa(big)), tt.stage("two", "cat"), tt.stage("three", "exit", "5")}
		}, "three: exit status 5"},
		{"signal", func(tt *tester) []Stage {
			return []Stage{tt.stage("one", "gen", strconv.Itoa(big)), tt.stage("two", "kill"), tt.stage("three", "cat")}
		}, "two: signal: killed"},
		{"reader gone", func(tt *tester) []Stage {
			return []Stage{tt.stage("one", "gen", strconv.Itoa(big)), tt.stage("two", "exit", "0")}
		}, "one: signal: broken pipe"},
		{"status allowed elsewhere", func(tt *tester) []Stage {
			tar := tt.stage("one", "exit", "2")
			tar.Allow = []int{1}
			return []Stage{tar}
		}, "one: exit status 2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := newTester(t)
			_, err := tt.run(t.Context(), Spec{Stages: tc.stages(tt)})
			if err == nil {
				t.Fatal("no error")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q, want %q in it", msg, tc.want)
			}
			// Only the cause is reported, not a stage that lost its
			// reader or its input.
			if n := strings.Count(msg, "stderr:"); n != 1 {
				t.Errorf("error %q names %d stages, want 1", msg, n)
			}
		})
	}
}

func TestAllowedStatus(t *testing.T) {
	tt := newTester(t)
	tar := tt.stage("src tar", "cat", "1")
	tar.Allow = []int{1}
	var logged []string
	var mu sync.Mutex
	in := writeFile(t, pattern(1000))
	out, err := tt.run(t.Context(), Spec{In: in, Stages: []Stage{tar}, Log: func(s string) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, s)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, pattern(1000)) {
		t.Error("output differs from the input")
	}
	if want := []string{"src tar: exit status 1, allowed"}; !equal(logged, want) {
		t.Errorf("logged %q, want %q", logged, want)
	}
}

func equal(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}

func TestCancel(t *testing.T) {
	tt := newTester(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	_, err := tt.run(ctx, Spec{Grace: 300 * time.Millisecond, Stages: []Stage{
		tt.stage("hang", "hang"),
		tt.stage("spawn", "spawn", "hang"),
		tt.stage("cat", "cat"),
	}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want context.Canceled", err)
	}
	if strings.Contains(fmt.Sprint(err), "stderr:") {
		t.Errorf("error %q blames a stage killed by the cancel", err)
	}
	if d := time.Since(start); d < 500*time.Millisecond {
		t.Errorf("returned after %v, before the grace for SIGTERM ran out", d)
	}
}

func TestFailureKillsHanging(t *testing.T) {
	tt := newTester(t)
	_, err := tt.run(t.Context(), Spec{Grace: 300 * time.Millisecond, Stages: []Stage{
		tt.stage("hang", "hang"),
		tt.stage("fail", "exit", "3"),
	}})
	if err == nil || !strings.Contains(err.Error(), "fail: exit status 3") || strings.Contains(err.Error(), "hang:") {
		t.Errorf("error %v, want only fail's exit status 3", err)
	}
}

// TestStageChild runs a stage that leaves a child holding its stdout and
// exits: the child is killed, so the next stage sees EOF.
func TestStageChild(t *testing.T) {
	tt := newTester(t)
	out, err := tt.run(t.Context(), Spec{Stages: []Stage{
		tt.stage("spawn", "spawn", "exit"),
		tt.stage("cat", "cat"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("output %q, want none", out)
	}
}

// TestEscapedChild runs a stage whose child left its process group and
// holds the stage's stderr: Wait still returns.
func TestEscapedChild(t *testing.T) {
	tt := newTester(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile) //nolint:gosec // G304: a file in the test's own temp dir
		if err != nil {
			t.Error(err)
			return
		}
		pid, err := strconv.Atoi(string(b))
		if err != nil {
			t.Error(err)
			return
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Error(err)
		}
	})
	start := time.Now()
	if _, err := tt.run(t.Context(), Spec{Stages: []Stage{tt.stage("escape", "escape", pidFile)}}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < stderrDrain {
		t.Errorf("returned after %v, before the stderr drain ran out", d)
	}
}

func TestStderrTail(t *testing.T) {
	tt := newTester(t)
	_, err := tt.run(t.Context(), Spec{Stages: []Stage{tt.stage("noisy", "stderr", "1000", "3")}})
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "noisy: exit status 3; stderr: …") ||
		!strings.HasSuffix(msg, "line 0999\nline 1000") || strings.Contains(msg, "line 0001") {
		t.Errorf("error %q, want the end of stderr only", msg)
	}
	if len(msg) > stderrTail+100 {
		t.Errorf("error of %d bytes, want the stderr cut to %d", len(msg), stderrTail)
	}
}

func TestStderrCopy(t *testing.T) {
	tt := newTester(t)
	var copied bytes.Buffer
	_, err := tt.run(t.Context(), Spec{Stderr: &copied, Stages: []Stage{
		tt.stage("one", "stderr", "2", "0"),
		tt.stage("two", "stderr", "1", "0"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(copied.String(), "line 0001\n"); got != 2 || !strings.Contains(copied.String(), "line 0002\n") {
		t.Errorf("copied stderr %q, want both stages' lines", copied.String())
	}
}

// writeFile returns a file holding b, open for reading.
func writeFile(t *testing.T, b []byte) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) //nolint:gosec // G304: a file in the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// createFile returns a new empty file open for writing.
func createFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func readFile(t *testing.T, f *os.File) []byte {
	t.Helper()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInputFile(t *testing.T) {
	tt := newTester(t)
	in := writeFile(t, pattern(big))
	out, err := tt.run(t.Context(), Spec{In: in, Stages: []Stage{tt.stage("flip", "flip")}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, flipped(pattern(big))) {
		t.Error("output differs from the input flipped")
	}
}

func TestOut(t *testing.T) {
	tt := newTester(t)
	dst := createFile(t)
	out, err := tt.run(t.Context(), Spec{Out: dst, Stages: []Stage{tt.stage("gen", "gen", strconv.Itoa(big))}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("Output gave %d bytes, want none", len(out))
	}
	if !bytes.Equal(readFile(t, dst), pattern(big)) {
		t.Error("written file differs from the output")
	}
}

func TestNoStages(t *testing.T) {
	t.Parallel()
	t.Run("output", func(t *testing.T) {
		t.Parallel()
		in := writeFile(t, pattern(1000))
		p, err := Start(t.Context(), Spec{In: in})
		if err != nil {
			t.Fatal(err)
		}
		if p.Output() != in {
			t.Error("Output is not the input")
		}
		if err := p.Wait(); err != nil {
			t.Error(err)
		}
		if err := in.Close(); err != nil {
			t.Error(err)
		}
	})
	t.Run("copy", func(t *testing.T) {
		t.Parallel()
		in := writeFile(t, pattern(big+1))
		dst := createFile(t)
		p, err := Start(t.Context(), Spec{In: in, Out: dst})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Wait(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readFile(t, dst), pattern(big+1)) {
			t.Error("copy differs from the input")
		}
		if err := in.Close(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("input not closed: %v", err)
		}
	})
	t.Run("cancelled copy", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		p, err := Start(ctx, Spec{In: writeFile(t, pattern(10)), Out: createFile(t)})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("error %v, want context.Canceled", err)
		}
	})
	t.Run("nothing", func(t *testing.T) {
		t.Parallel()
		if _, err := Start(t.Context(), Spec{}); err == nil {
			t.Error("no error")
		}
	})
}

func TestEmptyCommand(t *testing.T) {
	t.Parallel()
	_, err := Start(t.Context(), Spec{Stages: []Stage{{}}})
	if err == nil || err.Error() != "start (empty command): empty command" {
		t.Errorf("error %v, want start (empty command): empty command", err)
	}
}

func TestExtraFiles(t *testing.T) {
	tt := newTester(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("secret"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	st := tt.stage("openssl", "fd3")
	st.Files = []*os.File{r}
	out, err := tt.run(t.Context(), Spec{Stages: []Stage{st}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "secret" {
		t.Errorf("output %q, want what was written to descriptor 3", out)
	}
	if err := r.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("descriptor 3 not closed in the parent: %v", err)
	}
}

// TestStartFails checks that a stage that cannot start takes down the
// ones started before it and closes every descriptor handed over.
func TestStartFails(t *testing.T) {
	tt := newTester(t)
	in := writeFile(t, pattern(10))
	extra := writeFile(t, nil)
	later := tt.stage("three", "cat")
	later.Files = []*os.File{extra}
	_, err := Start(t.Context(), Spec{In: in, Stages: []Stage{
		tt.stage("one", "hang"),
		{Name: "two", Argv: []string{filepath.Join(t.TempDir(), "missing")}},
		later,
	}})
	if err == nil || !strings.HasPrefix(err.Error(), "start two: ") {
		t.Errorf("error %v, want start two", err)
	}
	if err := in.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("input not closed: %v", err)
	}
	if err := extra.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("descriptor of a stage never started not closed: %v", err)
	}
}
