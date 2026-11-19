package main

import (
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args    []string
		name    string
		pos     []string
		bytes   bool
		force   bool
		profile string
		file    string
		given   []string
	}{
		{args: []string{"a.yml", "b.yml"}, pos: []string{"a.yml", "b.yml"}},
		{args: []string{"list", "a.yml"}, name: "list", pos: []string{"a.yml"}},
		{args: []string{"--bytes", "list", "a.yml"}, name: "list", pos: []string{"a.yml"}, bytes: true, given: []string{"bytes"}},
		{args: []string{"list", "a.yml", "--bytes"}, name: "list", pos: []string{"a.yml"}, bytes: true, given: []string{"bytes"}},
		{args: []string{"list", "--bytes", "a.yml"}, name: "list", pos: []string{"a.yml"}, bytes: true, given: []string{"bytes"}},
		{args: []string{"-bytes", "list", "a.yml"}, name: "list", pos: []string{"a.yml"}, bytes: true, given: []string{"bytes"}},
		{args: []string{"--profile", "x", "list", "a.yml"}, name: "list", pos: []string{"a.yml"}, profile: "x", given: []string{"profile"}},
		{args: []string{"list", "a.yml", "--profile=x"}, name: "list", pos: []string{"a.yml"}, profile: "x", given: []string{"profile"}},
		{args: []string{"list", "a.yml", "--profile", "x", "--bytes"}, name: "list", pos: []string{"a.yml"}, profile: "x", bytes: true, given: []string{"bytes", "profile"}},
		{args: []string{"list-profiles", "a.yml"}, name: "list-profiles", pos: []string{"a.yml"}},
		{
			args: []string{"download", "a.yml", "a-2026-11-19-d.tar", "--filename", "-", "--force", "--profile", "a"},
			name: "download", pos: []string{"a.yml", "a-2026-11-19-d.tar"}, file: "-", force: true, profile: "a",
			given: []string{"filename", "force", "profile"},
		},
		// A first argument that is not a subcommand is a config.
		{args: []string{"lists", "a.yml"}, pos: []string{"lists", "a.yml"}},
		// -- ends the options.
		{args: []string{"--", "--bytes"}, pos: []string{"--bytes"}},
		{args: []string{"list", "--", "-a.yml"}, name: "list", pos: []string{"-a.yml"}},
		// A lone dash is an argument, not an option.
		{args: []string{"-"}, pos: []string{"-"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			c, err := parse(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if c.name != tt.name || !slices.Equal(c.args, tt.pos) {
				t.Errorf("subcommand %q args %q, want %q %q", c.name, c.args, tt.name, tt.pos)
			}
			o := c.opts
			if o.bytes != tt.bytes || o.force != tt.force || o.profile != tt.profile || o.filename != tt.file {
				t.Errorf("options %+v", o)
			}
			var given []string
			for name := range o.given {
				given = append(given, name)
			}
			slices.Sort(given)
			if !slices.Equal(given, tt.given) {
				t.Errorf("given %q, want %q", given, tt.given)
			}
		})
	}
}

func TestParseHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--help"}, {"-h"}, {"list", "a.yml", "--help"}, {"-h", "list"}} {
		c, err := parse(args)
		if err != nil || !c.opts.help {
			t.Errorf("parse %q: %+v, %v", args, c, err)
		}
	}
}

func TestParseVersion(t *testing.T) {
	t.Parallel()
	c, err := parse([]string{"--version"})
	if err != nil || !c.opts.version {
		t.Errorf("parse --version: %+v, %v", c, err)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--bytes"}, "no config given"},
		{[]string{"--frobnicate", "a.yml"}, "unknown option --frobnicate"},
		{[]string{"list", "a.yml", "--profile"}, "option --profile needs a value"},
		{[]string{"--bytes=maybe", "list", "a.yml"}, "invalid boolean value"},
		{[]string{"list", "a.yml", "--profile="}, "--profile needs a value"},
		{[]string{"download", "a.yml", "x", "--filename", ""}, "--filename needs a value"},
		{[]string{"--bytes", "a.yml"}, "--bytes is not an option of a backup"},
		{[]string{"list-profiles", "a.yml", "--profile", "x"}, "--profile is not an option of list-profiles"},
		{[]string{"list", "a.yml", "--force", "--filename=x"}, "--filename is not an option of list"},
		{[]string{"download", "a.yml", "--bytes", "x"}, "--bytes is not an option of download"},
		{[]string{"list"}, "list takes <config>"},
		{[]string{"list", "a.yml", "b.yml"}, "list takes <config>"},
		{[]string{"list-profiles"}, "list-profiles takes <config>"},
		{[]string{"download", "a.yml"}, "download takes <config> <archive>"},
		{[]string{"--version", "a.yml"}, "--version takes no other arguments"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			_, err := parse(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want one with %q", err, tt.want)
			}
		})
	}
}
