// Package run runs backups: every profile of every config, with attempts,
// cleanup after each, the post hook and the exit status of the run.
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/kopytov/go-bkp/internal/compress"
	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/crypt"
	"github.com/kopytov/go-bkp/internal/dst"
	"github.com/kopytov/go-bkp/internal/pipeline"
	"github.com/kopytov/go-bkp/internal/shape"
	"github.com/kopytov/go-bkp/internal/src"
)

const (
	// attempts is how many times a profile is tried.
	attempts = 10
	// pauseStep times the number of a failed attempt, from 0, is the pause
	// after it.
	pauseStep = 180 * time.Second
	// umountTimeout bounds the umount after an attempt, which runs even
	// when the run is cancelled.
	umountTimeout = 5 * time.Minute
	// postWaitDelay is how long the output of a post hook may stay open
	// after it was killed, through a process that left its group.
	postWaitDelay = 5 * time.Second
	// logTime is the time format of progress lines, as Raku prints a
	// DateTime truncated to seconds.
	logTime = "2006-01-02T15:04:05Z07:00"
)

// Classes returns the class tables of every namespace.
func Classes() config.Classes {
	return config.Classes{
		Src:      src.Classes,
		Compress: compress.Classes,
		Crypt:    crypt.Classes,
		Shape:    shape.Classes,
		Dst:      dst.Classes,
	}
}

// Runner runs the backups of configs.
type Runner struct {
	Classes config.Classes
	// Stdout gets progress lines, when Log is set, and the output of post
	// hooks.
	Stdout io.Writer
	// Stderr gets error messages and the output of post hooks; with Log,
	// also the stderr of stages.
	Stderr io.Writer
	// Log turns progress lines on, as BKP_LOG does.
	Log bool
	// PID is the process ID in progress lines.
	PID int
	// Now returns the current time; nil means time.Now. Archive names take
	// its local date.
	Now func() time.Time
	// Pause waits d or until ctx is done, between attempts; nil means a
	// timer.
	Pause func(ctx context.Context, d time.Duration) error
}

// Backup backs up every profile of every config in order and reports
// whether all configs loaded, all profiles were written and all post hooks
// succeeded. Failures go to Stderr and the run goes on with the next
// profile or config. When ctx is done, the current attempt is stopped and
// cleaned up and the rest is skipped.
func (r *Runner) Backup(ctx context.Context, configs []string) bool {
	for _, path := range configs {
		r.logf("Got configs: %s", path)
	}
	ok := true
	for _, path := range configs {
		if ctx.Err() != nil {
			return false
		}
		if !r.config(ctx, path) {
			ok = false
		}
	}
	return ok && ctx.Err() == nil
}

// config backs up the profiles of one config and reports whether it
// loaded and all of them succeeded.
func (r *Runner) config(ctx context.Context, path string) bool {
	base := filepath.Base(path)
	r.logf("%s started", base)
	profiles, err := config.Load(ctx, path, r.Classes)
	if err != nil {
		r.errorf("%s loading failed - skipping\n%v", base, err)
		return false
	}
	for _, p := range profiles {
		r.logf("Got profiles: %s", p.Name)
	}
	ok := true
	for _, p := range profiles {
		if ctx.Err() != nil {
			return false
		}
		if !r.profile(ctx, base+":"+p.Name, p) {
			ok = false
		}
	}
	r.logf("%s finished", base)
	return ok
}

// profile makes the archive of p, retrying failed attempts, runs its post
// hook and reports whether both succeeded. name is <config>:<profile>.
func (r *Runner) profile(ctx context.Context, name string, p config.Profile) bool {
	for attempt := range attempts {
		r.logf("%s started [%d]", name, attempt)
		written, err := r.attempt(ctx, name, p)
		if written && err != nil {
			// The archive is in place: report what failed after it and
			// do not send it again.
			for _, e := range flatten(err) {
				r.errorf("%s %v", name, e)
			}
			r.logf("%s finished [%d]", name, attempt)
			if ctx.Err() == nil {
				r.post(ctx, name, p)
			}
			return false
		}
		if err == nil {
			r.logf("%s finished [%d]", name, attempt)
			return r.post(ctx, name, p)
		}
		r.errorf("%s failed [%d]\n%s exception: %v", name, attempt, name, err)
		if ctx.Err() != nil {
			return false
		}
		if attempt == attempts-1 {
			break
		}
		if err := r.pause(ctx, time.Duration(attempt)*pauseStep); err != nil {
			return false
		}
	}
	r.errorf("BACKUP %s FAILED!", name)
	return false
}

// attempt writes the archive of p once and rotates the destination. The
// destination is released afterwards whatever happened, even when ctx is
// done, for at most umountTimeout. written reports whether the archive is in
// place; err is then what failed after it: rotation or the release.
func (r *Runner) attempt(ctx context.Context, name string, p config.Profile) (written bool, err error) {
	defer func() {
		if uerr := umount(ctx, p.Dst); uerr != nil {
			if written {
				err = errors.Join(err, fmt.Errorf("cleanup failed: dst: %w", uerr))
			} else {
				err = errors.Join(err, fmt.Errorf("dst: %w", uerr))
			}
		}
	}()
	return r.write(ctx, name, p)
}

// write writes the archive of p and rotates the destination; see attempt.
func (r *Runner) write(ctx context.Context, name string, p config.Profile) (written bool, err error) {
	if err := p.Dst.Mount(ctx); err != nil {
		return false, fmt.Errorf("dst: %w", err)
	}
	entries, err := p.Dst.List(ctx)
	if err != nil {
		return false, fmt.Errorf("dst: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	naming := p.Dst.Naming(p.Suffix())
	archive, err := naming.Next(names, r.now())
	if err != nil {
		return false, fmt.Errorf("dst: %w", err)
	}
	r.logf("%s writing %s", name, archive)
	var sendErr error
	err = p.Dst.Write(ctx, archive, func(out *os.File) error {
		sendErr = r.send(ctx, name, p, out)
		return sendErr
	})
	if err != nil {
		if sendErr == nil {
			// The destination's own failure, not the pipeline's.
			err = fmt.Errorf("dst: %w", err)
		}
		return errors.Is(err, config.ErrInPlace), err
	}
	return true, r.rotate(ctx, name, p.Dst, naming, names, archive)
}

// rotate deletes the archives that written replaces and, unless no-rotate,
// those beyond the plan. It tries every one and returns what failed, an
// error for each.
func (r *Runner) rotate(ctx context.Context, name string, d config.Dest, naming config.Naming, names []string, written string) error {
	replaced, err := naming.Replaced(names, written)
	if err != nil {
		return fmt.Errorf("rotate failed: dst: %w", err)
	}
	rotated, err := naming.Rotate(names, written)
	if err != nil {
		return fmt.Errorf("rotate failed: dst: %w", err)
	}
	var errs []error
	for _, old := range slices.Concat(replaced, rotated) {
		r.logf("%s deleting %s", name, old)
		if err := d.Delete(ctx, old); err != nil {
			errs = append(errs, fmt.Errorf("rotate failed: dst: %w", err))
		}
	}
	return errors.Join(errs...)
}

// flatten returns the errors joined in err by errors.Join, at any depth, so
// that each is reported on a line of its own.
func flatten(err error) []error {
	j, ok := err.(interface{ Unwrap() []error }) // only a join made here is split, not one wrapped inside another error
	if !ok {
		return []error{err}
	}
	var out []error
	for _, e := range j.Unwrap() {
		out = append(out, flatten(e)...)
	}
	return out
}

// send runs the pipeline of p into out and waits for every stage.
func (r *Runner) send(ctx context.Context, name string, p config.Profile, out *os.File) error {
	spec, err := p.Spec()
	if err != nil {
		return err
	}
	spec.Out = out
	if r.Log {
		spec.Stderr = r.Stderr
	}
	spec.Log = func(msg string) { r.logf("%s %s", name, msg) }
	pl, err := pipeline.Start(ctx, spec)
	if err != nil {
		return err
	}
	return pl.Wait()
}

// post runs the post hook of p, if it has one, and reports whether it
// succeeded. It is not retried.
func (r *Runner) post(ctx context.Context, name string, p config.Profile) bool {
	if p.Post == "" {
		return true
	}
	r.logf("%s post hook started", name)
	cmd := exec.CommandContext(ctx, "bash", "-o", "pipefail", "-c", p.Post)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	// In a group of its own, so that a cancel kills what it started too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = postWaitDelay
	if err := cmd.Run(); err != nil {
		r.errorf("%s post hook failed: %v", name, err)
		return false
	}
	r.logf("%s post hook finished", name)
	return true
}

func (r *Runner) pause(ctx context.Context, d time.Duration) error {
	if r.Pause != nil {
		return r.Pause(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// logf writes a progress line, when Log is set, in the format of Raku:
// <time> bkp: [<pid>] <message>.
func (r *Runner) logf(format string, args ...any) {
	if !r.Log {
		return
	}
	fmt.Fprintf(r.Stdout, "%s bkp: [%d] %s\n", r.now().Format(logTime), r.PID, fmt.Sprintf(format, args...)) //nolint:errcheck // nowhere to report a failed write of the log
}

// errorf writes an error message to Stderr.
func (r *Runner) errorf(format string, args ...any) {
	fmt.Fprintf(r.Stderr, format+"\n", args...) //nolint:errcheck // nowhere to report a failed write to stderr
}
