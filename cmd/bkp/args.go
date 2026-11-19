package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// options are the named options of every subcommand. Each subcommand
// accepts only some of them; see command.
type options struct {
	bytes    bool
	force    bool
	version  bool
	help     bool
	profile  string
	filename string
	// given holds the names of the options on the command line.
	given map[string]bool
}

// command is a parsed command line: the subcommand, empty for a backup, its
// positional arguments and the options.
type command struct {
	name string
	args []string
	opts options
}

func newFlags(o *options) *flag.FlagSet {
	fs := flag.NewFlagSet("bkp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.bytes, "bytes", false, "")
	fs.BoolVar(&o.force, "force", false, "")
	fs.BoolVar(&o.version, "version", false, "")
	fs.BoolVar(&o.help, "help", false, "")
	fs.BoolVar(&o.help, "h", false, "")
	fs.StringVar(&o.profile, "profile", "", "")
	fs.StringVar(&o.filename, "filename", "", "")
	return fs
}

// parse parses args: named options may stand anywhere, before the
// subcommand too, as --opt, --opt value or --opt=value; -- ends them.
// --help or -h anywhere asks for the usage and nothing else. The first positional argument names the subcommand; one that
// is not a subcommand is the first config of a backup. An error means a
// command line that fits no subcommand.
func parse(args []string) (command, error) {
	var c command
	fs := newFlags(&c.opts)
	opts, pos, err := split(fs, args)
	if err != nil {
		return c, err
	}
	if err := fs.Parse(opts); err != nil {
		return c, err
	}
	c.opts.given = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { c.opts.given[f.Name] = true })

	if c.opts.help {
		return c, nil
	}
	for _, name := range []string{"profile", "filename"} {
		if c.opts.given[name] && fs.Lookup(name).Value.String() == "" {
			return c, fmt.Errorf("--%s needs a value", name)
		}
	}
	if c.opts.version {
		if len(args) != 1 {
			return c, errors.New("--version takes no other arguments")
		}
		return c, nil
	}
	if len(pos) == 0 {
		return c, errors.New("no config given")
	}
	var want []string
	switch pos[0] {
	case "list", "list-profiles":
		c.name, c.args, want = pos[0], pos[1:], []string{"<config>"}
	case "download":
		c.name, c.args, want = pos[0], pos[1:], []string{"<config>", "<archive>"}
	default:
		c.args = pos
	}
	if want != nil && len(c.args) != len(want) {
		return c, fmt.Errorf("%s takes %s", c.name, strings.Join(want, " "))
	}
	for _, name := range slices.Sorted(maps.Keys(c.opts.given)) {
		if !slices.Contains(c.accepts(), name) {
			return c, fmt.Errorf("--%s is not an option of %s", name, c.title())
		}
	}
	return c, nil
}

// accepts returns the options the subcommand of c takes; a backup takes
// none.
func (c command) accepts() []string {
	switch c.name {
	case "list":
		return []string{"bytes", "profile"}
	case "download":
		return []string{"profile", "filename", "force"}
	}
	return nil
}

// title names the subcommand of c in messages.
func (c command) title() string {
	if c.name == "" {
		return "a backup"
	}
	return c.name
}

// split separates the options in args, each with its value, from the
// positional arguments. It asks fs whether an option takes a value.
func split(fs *flag.FlagSet, args []string) (opts, pos []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return opts, append(pos, args[i+1:]...), nil
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		opts = append(opts, a)
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			return nil, nil, fmt.Errorf("unknown option %s", a)
		}
		if hasValue || isBool(f) {
			continue
		}
		if i+1 == len(args) {
			return nil, nil, fmt.Errorf("option %s needs a value", a)
		}
		i++
		opts = append(opts, args[i])
	}
	return opts, pos, nil
}

// isBool reports whether f is a flag given without a value.
func isBool(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}
