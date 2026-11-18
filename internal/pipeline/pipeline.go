// Package pipeline runs a chain of stage processes, the output of each the
// input of the next, so that the data passes from process to process and
// never through bkp. It waits for every stage, checks the exit status of
// each and kills them all on a failure or a cancel.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/kopytov/go-bkp/internal/tail"
)

const (
	// defaultGrace is how long stages have to exit after SIGTERM before
	// they get SIGKILL.
	defaultGrace = 5 * time.Second
	// settle is how long the first failure waits for other stages to
	// exit before the rest are killed, so that a stage that died of
	// SIGPIPE behind the failed one is told from the cause.
	settle = 100 * time.Millisecond
	// stderrTail bounds the stderr of a failed stage quoted in its error.
	stderrTail = 4096
	// stderrDrain is how long the stderr of a stage may stay open after
	// every stage exited, through a process that left its group.
	stderrDrain = 2 * time.Second
	// copyChunk is how much a pipeline without stages copies between
	// checks for a cancel.
	copyChunk = 64 << 20

	// pPID selects a process by pid in waitid.
	pPID = 1
	// siginfoSize is the size of siginfo_t on Linux.
	siginfoSize = 128
)

// Stage is an external process of a pipeline.
type Stage struct {
	// Name names the stage in errors and the log, such as "compress gzip".
	Name string
	// Argv is the command, run without a shell.
	Argv []string
	// Allow lists exit statuses besides 0 that count as success, such as
	// 1 of tar. They are logged.
	Allow []int
	// Files become the stage's descriptors 3, 4 and on. Start closes
	// them once the stage started, or failed to.
	Files []*os.File
}

// Spec describes a pipeline.
type Spec struct {
	// In is the input of the first stage, or the whole output when there
	// are no stages. Nil gives the first stage /dev/null. Start closes it,
	// unless there are no stages and no Out: then it is the Output.
	In *os.File
	// Stages run in order, each reading the output of the one before.
	Stages []Stage
	// Out receives the output of the last stage, or a copy of In when
	// there are no stages. It stays open. When it is nil, the output is
	// read from Output.
	Out *os.File
	// Stderr, when set, gets a copy of the stderr of every stage.
	Stderr io.Writer
	// Log, when set, gets a line for each stage that exited with a status
	// it allows besides 0. It may be called from another goroutine, never
	// from two at once.
	Log func(string)
	// Grace is how long stages have to exit after SIGTERM before they
	// get SIGKILL; 0 means 5 seconds.
	Grace time.Duration
}

// Pipeline is a started pipeline.
type Pipeline struct {
	spec   Spec
	procs  []*proc
	out    *os.File
	copyTo *lockedWriter // nil: stage stderr is not copied
	drain  sync.WaitGroup
	done   chan struct{}
	err    error
}

// proc is a started stage.
type proc struct {
	Stage
	cmd     *exec.Cmd
	stderr  *tail.Buffer
	errR    *os.File // the read end of its stderr
	exited  bool     // its group killed and it reaped; owned by supervise
	waitErr error    // set when it could not be waited for
}

// Start starts the stages of s and returns without waiting for them. It
// closes s.In and the Files of every stage, whether it succeeds or not.
//
// Each stage runs in a process group of its own. When a stage exits, its
// group is killed, so a process it left behind cannot hold the input of
// the next stage open. When a stage fails or ctx is done, the groups of
// the stages still running get SIGTERM and, after s.Grace, SIGKILL.
//
// Whoever reads Output must read it to the end or cancel ctx; a stage
// that cannot write never exits.
func Start(ctx context.Context, s Spec) (*Pipeline, error) {
	if s.Grace == 0 {
		s.Grace = defaultGrace
	}
	p := &Pipeline{spec: s, done: make(chan struct{})}
	if s.Stderr != nil {
		p.copyTo = &lockedWriter{w: s.Stderr}
	}
	if len(s.Stages) == 0 {
		switch {
		case s.In == nil:
			return nil, errors.New("pipeline without stages needs an input")
		case s.Out == nil:
			p.out = s.In
			close(p.done)
		default:
			go p.copy(ctx)
		}
		return p, nil
	}
	in := s.In
	for i, st := range s.Stages {
		next, err := p.startStage(ctx, st, in, i == len(s.Stages)-1)
		if err != nil {
			for _, later := range s.Stages[i+1:] {
				closeFiles(later.Files...)
			}
			p.abort()
			return nil, fmt.Errorf("start %s: %w", st.name(), err)
		}
		in = next
	}
	go p.supervise(ctx)
	return p, nil
}

// Output returns the read end of the output when Spec.Out is nil, or
// Spec.In when there are no stages. The caller closes it.
func (p *Pipeline) Output() *os.File {
	return p.out
}

// Wait waits for every stage and returns an error naming each stage that
// failed, with its exit status or signal and the end of its stderr, or
// the error of ctx when the pipeline was cancelled.
func (p *Pipeline) Wait() error {
	<-p.done
	return p.err
}

// startStage starts st reading from in and returns the read end of its
// output, or nil for the last stage. It closes in and st.Files either way.
func (p *Pipeline) startStage(ctx context.Context, st Stage, in *os.File, last bool) (*os.File, error) {
	defer closeFiles(append([]*os.File{in}, st.Files...)...)
	if len(st.Argv) == 0 {
		return nil, errors.New("empty command")
	}
	stdout := p.spec.Out
	var r *os.File
	if !last || stdout == nil {
		var w *os.File
		var err error
		if r, w, err = os.Pipe(); err != nil {
			return nil, err
		}
		// The parent's copy: left open, the reader never sees EOF.
		defer closeFiles(w)
		stdout = w
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeFiles(r)
		return nil, err
	}
	defer closeFiles(errW)

	cmd := exec.CommandContext(ctx, st.Argv[0], st.Argv[1:]...)
	if in != nil {
		cmd.Stdin = in
	}
	cmd.Stdout = stdout
	cmd.Stderr = errW
	cmd.ExtraFiles = st.Files
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// supervise alone signals the stages, and only before it reaps them,
	// so that a signal never reaches a pid that is free again.
	cmd.Cancel = func() error { return nil }
	if err := cmd.Start(); err != nil {
		closeFiles(r, errR)
		return nil, err
	}
	pr := &proc{Stage: st, cmd: cmd, stderr: tail.New(stderrTail), errR: errR}
	p.procs = append(p.procs, pr)
	p.drain.Add(1)
	go p.readStderr(pr)
	if last {
		p.out = r
		return nil, nil
	}
	return r, nil
}

// readStderr keeps the end of the stderr of pr and copies it to
// Spec.Stderr until EOF or the drain deadline.
func (p *Pipeline) readStderr(pr *proc) {
	defer p.drain.Done()
	w := &sink{tail: pr.stderr, copyTo: p.copyTo}
	if _, err := io.Copy(w, pr.errR); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		w.note(fmt.Sprintf("(reading stderr: %v)", err))
	}
}

// supervise waits for every stage, kills the rest on a failure or a
// cancel, and sets the result of Wait.
func (p *Pipeline) supervise(ctx context.Context) {
	defer close(p.done)
	exits := make(chan *proc, len(p.procs))
	for _, pr := range p.procs {
		go func() {
			if err := waitExit(pr.cmd.Process.Pid); err != nil {
				// Not left a zombie: reap it here, so that supervise
				// never blocks on a stage still running.
				pr.waitErr = err
				if err := pr.cmd.Wait(); err != nil && pr.cmd.ProcessState == nil {
					pr.waitErr = errors.Join(pr.waitErr, err)
				}
			}
			exits <- pr
		}()
	}

	var (
		failed    []*proc // failures before the kill, in the order seen
		cancelled error
		killing   bool
		settled   <-chan time.Time
		kill      <-chan time.Time
	)
	ctxDone := ctx.Done()
	terminate := func() {
		if killing {
			return
		}
		killing = true
		settled = nil
		p.signalRunning(syscall.SIGTERM)
		kill = time.After(p.spec.Grace)
	}
	for left := len(p.procs); left > 0; {
		select {
		case pr := <-exits:
			left--
			p.reap(pr)
			switch {
			case killing:
				// Its status may be our doing.
			case !pr.ok():
				failed = append(failed, pr)
				if settled == nil {
					settled = time.After(settle)
				}
			case pr.cmd.ProcessState.ExitCode() != 0:
				p.log(fmt.Sprintf("%s: %s, allowed", pr.name(), pr.cmd.ProcessState))
			}
		case <-settled:
			terminate()
		case <-ctxDone:
			ctxDone = nil
			cancelled = ctx.Err()
			terminate()
		case <-kill:
			kill = nil
			p.signalRunning(syscall.SIGKILL)
		}
	}
	p.finishStderr()
	p.err = result(failed, cancelled)
}

// reap kills what the exited stage pr left in its group, so that nothing
// keeps writing or holds a pipe open, and then reaps pr. Until then pr is
// a zombie holding its pid, so the signal cannot reach another process.
func (p *Pipeline) reap(pr *proc) {
	pr.exited = true
	if pr.waitErr != nil {
		return // reaped by its waiter already
	}
	p.logSignalErr(signalGroup(pr.cmd.Process.Pid, syscall.SIGKILL))
	if err := pr.cmd.Wait(); err != nil && pr.cmd.ProcessState == nil {
		pr.waitErr = err
	}
}

// waitExit blocks until the process pid exits and leaves it unreaped, so
// that its pid stays reserved.
func waitExit(pid int) error {
	var info [siginfoSize]byte
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid),
			uintptr(unsafe.Pointer(&info)), syscall.WEXITED|syscall.WNOWAIT, 0, 0) //nolint:gosec // G103: siginfo_t buffer for the waitid syscall, which syscall does not wrap
		switch errno {
		case 0:
			return nil
		case syscall.EINTR:
		default:
			return fmt.Errorf("waitid: %w", errno)
		}
	}
}

// result builds the error of Wait. A stage killed by SIGPIPE is left out
// when another one failed: it only lost its reader.
func result(failed []*proc, cancelled error) error {
	cause := slices.DeleteFunc(slices.Clone(failed), (*proc).sigpipe)
	if len(cause) == 0 {
		cause = failed
	}
	errs := make([]error, 0, len(cause)+1)
	for _, pr := range cause {
		errs = append(errs, pr.failure())
	}
	if cancelled != nil {
		errs = append(errs, fmt.Errorf("pipeline stopped: %w", cancelled))
	}
	return errors.Join(errs...)
}

// abort kills and reaps the stages started so far, after a stage failed to
// start.
func (p *Pipeline) abort() {
	for _, pr := range p.procs {
		p.logSignalErr(signalGroup(pr.cmd.Process.Pid, syscall.SIGKILL))
		pr.cmd.Wait() //nolint:errcheck,gosec // killed on purpose, only reaped here
	}
	p.finishStderr()
}

// finishStderr waits for the stderr readers, at most stderrDrain for a
// stderr held open by a process that left its stage's group, and closes
// the read ends.
func (p *Pipeline) finishStderr() {
	deadline := time.Now().Add(stderrDrain)
	for _, pr := range p.procs {
		if err := pr.errR.SetReadDeadline(deadline); err != nil {
			// Not pollable, so a deadline cannot end the read; the
			// close below may still.
			closeFiles(pr.errR)
		}
	}
	p.drain.Wait()
	for _, pr := range p.procs {
		closeFiles(pr.errR)
	}
}

// signalRunning sends sig to the group of every stage not yet reaped. The
// group of a reaped stage is left alone: its pid may belong to another
// process by now. A stage that exited but is not reaped yet still holds
// its pid, so signalling its group is safe.
func (p *Pipeline) signalRunning(sig syscall.Signal) {
	for _, pr := range p.procs {
		if !pr.exited {
			p.logSignalErr(signalGroup(pr.cmd.Process.Pid, sig))
		}
	}
}

// copy copies Spec.In to Spec.Out for a pipeline without stages. On Linux
// the kernel copies the data; ctx is checked between chunks.
func (p *Pipeline) copy(ctx context.Context) {
	defer close(p.done)
	defer closeFiles(p.spec.In)
	for {
		if err := ctx.Err(); err != nil {
			p.err = fmt.Errorf("copy %s: %w", p.spec.In.Name(), err)
			return
		}
		_, err := io.CopyN(p.spec.Out, p.spec.In, copyChunk)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			p.err = fmt.Errorf("copy %s: %w", p.spec.In.Name(), err)
			return
		}
	}
}

func (p *Pipeline) log(msg string) {
	if p.spec.Log != nil {
		p.spec.Log(msg)
	}
}

func (p *Pipeline) logSignalErr(err error) {
	if err != nil {
		p.log(err.Error())
	}
}

func (pr *proc) name() string { return pr.Stage.name() }

func (st Stage) name() string {
	switch {
	case st.Name != "":
		return st.Name
	case len(st.Argv) == 0:
		return "(empty command)"
	}
	return st.Argv[0]
}

// ok reports whether pr exited with 0 or a status it allows.
func (pr *proc) ok() bool {
	ps := pr.cmd.ProcessState
	if ps == nil {
		return false
	}
	code := ps.ExitCode() // -1 when killed by a signal
	return code == 0 || code > 0 && slices.Contains(pr.Allow, code)
}

// sigpipe reports whether pr was killed by SIGPIPE.
func (pr *proc) sigpipe() bool {
	ps := pr.cmd.ProcessState
	if ps == nil {
		return false
	}
	ws, ok := ps.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGPIPE
}

// failure describes how pr failed.
func (pr *proc) failure() error {
	if pr.waitErr != nil {
		return fmt.Errorf("%s: %w; stderr: %s", pr.name(), pr.waitErr, pr.stderr)
	}
	return fmt.Errorf("%s: %s; stderr: %s", pr.name(), pr.cmd.ProcessState, pr.stderr)
}

// signalGroup sends sig to the process group pgid; a group that is gone
// already is no error.
func signalGroup(pgid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("send %s to process group %d: %w", sig, pgid, err)
	}
	return nil
}

// closeFiles closes our copies of descriptors; nil ones are skipped.
func closeFiles(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			f.Close() //nolint:errcheck,gosec // our copy of a pipe end or a file only read from: nothing to lose
		}
	}
}

// sink keeps the end of a stage's stderr and copies it on.
type sink struct {
	tail   *tail.Buffer
	copyTo *lockedWriter
}

func (s *sink) Write(b []byte) (int, error) {
	if _, err := s.tail.Write(b); err != nil {
		return 0, err
	}
	if s.copyTo != nil {
		s.copyTo.Write(b)
	}
	return len(b), nil
}

// note adds msg to the kept stderr.
func (s *sink) note(msg string) {
	if _, err := s.Write([]byte("\n" + msg)); err != nil {
		panic(err) // tail.Buffer never fails
	}
}

// lockedWriter serializes the stderr copies of several stages and stops
// copying after the first failed write: losing the copy must not stop the
// stages.
type lockedWriter struct {
	mu     sync.Mutex
	w      io.Writer
	broken bool
}

func (l *lockedWriter) Write(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.broken {
		return
	}
	if _, err := l.w.Write(b); err != nil {
		l.broken = true
	}
}
