package dst

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/tail"
)

const (
	// toolOutput bounds the output of a helper tool quoted in its error.
	toolOutput = 4096
	// toolWaitDelay is how long the output of a helper tool may stay open
	// after it exited, through a process it left running, such as sshfs.
	toolWaitDelay = 5 * time.Second
)

// Common holds the attributes every dst class has. Classes embed it inline.
type Common struct {
	Plan     Plan `yaml:"plan"`
	NoRotate bool `yaml:"no-rotate"`
	// ArchivePrefix is set by config loading: dst.prefix or the profile
	// name.
	ArchivePrefix string `yaml:"prefix"`
}

// check checks the common attributes after decoding.
func (c *Common) check() error {
	if c.Plan == (Plan{}) {
		return errors.New("plan is required")
	}
	if c.ArchivePrefix == "" {
		return errors.New("prefix is empty")
	}
	return nil
}

// Prefix implements config.Dest.
func (c *Common) Prefix() string { return c.ArchivePrefix }

// Naming implements config.Dest.
func (c *Common) Naming(suffix string) config.Naming {
	return Namer{Prefix: c.ArchivePrefix, Suffix: suffix, Plan: c.Plan, NoRotate: c.NoRotate}
}

// checkBase checks that name is a file name of its own, which cannot lead
// out of the destination.
func checkBase(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("%q is not a file name in the destination", name)
	}
	return nil
}

// runTool runs a helper tool, such as mount, and returns an error with its
// exit status and the end of its output when it fails. The error names the
// program only: the rest of argv may hold a password, as mount -o does.
func runTool(ctx context.Context, argv []string) error {
	out := tail.New(toolOutput)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = toolWaitDelay
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState.Success() {
		// A daemon it started holds the output open; the tool itself
		// succeeded.
		err = nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w; output: %s", argv[0], err, out)
	}
	return nil
}
