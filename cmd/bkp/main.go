// Command bkp takes backups: bkp <config>... runs every profile of every
// config. Its subcommands list the archives and profiles of a config and
// download an archive.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kopytov/go-bkp/internal/run"
)

const usage = `usage: bkp <config>...
       bkp list <config> [--bytes] [--profile <name>]
       bkp list-profiles <config>
       bkp download <config> <archive> [--profile <name>] [--filename <path>|-] [--force]
       bkp --version
`

// version is set by scripts/build.sh.
var version = "dev"

func main() {
	os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr, nil))
}

// cli runs bkp with args and returns the exit status: 0 on success and for
// --help, 1 on a
// failure, such as a backup that was not taken, 128 plus the signal number
// when SIGTERM or SIGINT stopped a backup, and 2 for a usage error. pause
// replaces the pause between attempts when not nil.
func cli(args []string, stdout, stderr io.Writer, pause func(context.Context, time.Duration) error) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage) //nolint:errcheck // nowhere to report a failed write to stderr
		return 2
	}
	c, err := parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "bkp: %v\n%s", err, usage) //nolint:errcheck // nowhere to report a failed write to stderr
		return 2
	}
	if c.opts.help {
		fmt.Fprint(stdout, usage) //nolint:errcheck // nowhere to report a failed write to stdout
		return 0
	}
	if c.opts.version {
		fmt.Fprintln(stdout, version) //nolint:errcheck // nowhere to report a failed write to stdout
		return 0
	}
	if c.name == "" {
		return backup(c.args, stdout, stderr, pause)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	switch c.name {
	case "list":
		err = run.List(ctx, stdout, run.Classes(), c.args[0], c.opts.bytes, c.opts.profile)
	case "list-profiles":
		err = run.ListProfiles(ctx, stdout, run.Classes(), c.args[0])
	default:
		panic("parse returned unknown subcommand " + c.name)
	}
	if err != nil {
		fmt.Fprintf(stderr, "bkp: %v\n", err) //nolint:errcheck // nowhere to report a failed write to stderr
		return 1
	}
	return 0
}

// backup runs bkp <config>...; see cli for its exit status.
func backup(configs []string, stdout, stderr io.Writer, pause func(context.Context, time.Duration) error) int {
	// The stages run in process groups of their own, so a signal to the
	// terminal's group reaches only bkp, which stops them. Later signals
	// are caught too, so that bkp is not killed before it cleaned up.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan syscall.Signal, 1)
	go func() {
		select {
		case s := <-sigs:
			got <- s.(syscall.Signal) //nolint:forcetypeassert // Notify above delivers only syscall signals
			cancel()
		case <-ctx.Done():
		}
	}()

	r := &run.Runner{
		Classes: run.Classes(),
		Stdout:  stdout,
		Stderr:  stderr,
		Log:     os.Getenv("BKP_LOG") != "",
		PID:     os.Getpid(),
		Pause:   pause,
	}
	ok := r.Backup(ctx, configs)
	select {
	case s := <-got:
		fmt.Fprintf(stderr, "bkp: stopped by %s\n", signalName(s)) //nolint:errcheck // nowhere to report a failed write to stderr
		return 128 + int(s)
	default:
	}
	if !ok {
		return 1
	}
	return 0
}

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	}
	return s.String()
}
