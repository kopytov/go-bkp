package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

var (
	// itemRE matches the placeholder of a template.
	itemRE = regexp.MustCompile(`\{\{\s*item\s*\}\}`)
	// itemOK matches a script output line that is safe to put into YAML.
	itemOK = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

const (
	// stderrTail bounds the stderr of a failed script quoted in its error.
	stderrTail = 4096
	// maxOutput bounds the output of a script, a list of source names.
	maxOutput = 1 << 20
	// waitDelay is how long the output of a script that exited or was
	// killed may stay open through a process it left behind.
	waitDelay = 2 * time.Second
)

// expand turns a src of script and template into the mapping of sources the
// Raku version makes: the script runs under bash -o pipefail, each line of
// its output replaces {{ item }} in a copy of the template, and the copies
// together are read as YAML.
//
// Unlike the Raku version, a script that fails or prints nothing is an
// error, and so is a line with anything but A-Z a-z 0-9 . _ -, which would
// change the meaning of the YAML.
func expand(ctx context.Context, src *yaml.Node) (*yaml.Node, error) {
	var script, template string
	for i := 0; i+1 < len(src.Content); i += 2 {
		k, v := src.Content[i], src.Content[i+1]
		var out *string
		switch k.Value {
		case "script":
			out = &script
		case "template":
			out = &template
		default:
			return nil, fmt.Errorf("line %d: unknown key %q beside script, want template", k.Line, k.Value)
		}
		if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" || v.Value == "" {
			return nil, fmt.Errorf("line %d: %s: want a string, got %s", v.Line, k.Value, given(v))
		}
		*out = v.Value
	}
	if template == "" {
		return nil, fmt.Errorf("line %d: template is required beside script", src.Line)
	}
	items, err := runScript(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}
	n, err := render(template, items)
	if err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	return n, nil
}

// runScript runs script and returns the lines of its output.
func runScript(ctx context.Context, script string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-o", "pipefail", "-c", script)
	stdout := &capped{max: maxOutput}
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Its own process group, so that a cancel kills the whole pipeline
	// the script started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	if cmd.Process != nil {
		// Whatever the script left running in the background goes too.
		if kerr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); kerr != nil &&
			!errors.Is(kerr, syscall.ESRCH) && err == nil {
			err = fmt.Errorf("kill what the script left running: %w", kerr)
		}
	}
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, exec.ErrWaitDelay):
		return nil, errors.New("exited, but left a background process holding its output open; " +
			"redirect its output or wait for it")
	case err != nil:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("%w; stderr: %s", err, stderr)
		}
		return nil, err
	case stdout.over:
		return nil, fmt.Errorf("printed more than %d bytes", maxOutput)
	}
	out := strings.TrimSuffix(stdout.buf.String(), "\n")
	if strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("printed nothing; stderr: %s", stderr)
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !itemOK.MatchString(line) {
			return nil, fmt.Errorf("output line %d %q: only A-Z a-z 0-9 . _ - are allowed", i+1, line)
		}
	}
	return lines, nil
}

// capped keeps the first max bytes written to it and notes whether more
// came.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); len(p) > room {
		c.over = true
		c.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// tailBuffer keeps about the last max bytes written to it.
type tailBuffer struct {
	b       []byte
	max     int
	dropped bool // the start of what was written is gone
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2*t.max {
		t.b = append(t.b[:0], t.b[len(t.b)-t.max:]...)
		t.dropped = true
	}
	return len(p), nil
}

// String returns the last max bytes, cut at a character boundary, for an
// error message.
func (t *tailBuffer) String() string {
	b := bytes.TrimRight(t.b, " \t\r\n")
	dropped := t.dropped || len(b) > t.max
	if len(b) > t.max {
		b = b[len(b)-t.max:]
	}
	if dropped {
		for len(b) > 0 && !utf8.RuneStart(b[0]) {
			b = b[1:]
		}
	}
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0:
		return "(empty)"
	case dropped:
		return "…" + string(b)
	}
	return string(b)
}

// render puts each item into a copy of template and reads the copies as one
// YAML mapping. A copy gets a line break at its end if the template lacks
// one, so that items do not run into each other.
func render(template string, items []string) (*yaml.Node, error) {
	if !strings.HasSuffix(template, "\n") {
		template += "\n"
	}
	var b strings.Builder
	for _, item := range items {
		b.WriteString(itemRE.ReplaceAllLiteralString(template, item))
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(b.String()), &doc); err != nil {
		return nil, fmt.Errorf("rendered YAML: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("rendered YAML is empty")
	}
	n, err := flatten(doc.Content[0])
	if err != nil {
		return nil, fmt.Errorf("rendered YAML: %w", err)
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("rendered YAML: want a mapping of sources, got %s", given(n))
	}
	return n, nil
}
