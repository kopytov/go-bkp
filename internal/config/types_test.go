package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type mount struct {
	Point string  `yaml:"point"`
	Cmd   Command `yaml:"cmd"`
}

type base struct {
	Base int `yaml:"base"`
}

type sample struct {
	base   `yaml:",inline"`
	Mount  mount            `yaml:"mount"`
	Items  []mount          `yaml:"items"`
	ByName map[string]mount `yaml:"byname"`
	Raw    yaml.Node        `yaml:"raw"`
	Files  Strings          `yaml:"files"`
	Cmd    Command          `yaml:"cmd"`
	Cwd    Dir              `yaml:"cwd"`
	Dump   File             `yaml:"dump"`
	Name   string           `yaml:"name"`
}

func decode(t *testing.T, text string) (sample, error) {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(text), &n); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	var s sample
	err := Decode(n.Content[0], &s)
	return s, err
}

// TestDecodeAliases decodes a section whose values are anchored in another
// part of the file, as a config may share them between sources.
func TestDecodeAliases(t *testing.T) {
	t.Parallel()
	var doc yaml.Node
	text := "common: &c [x, y]\nmore: &m {cmd: [tar, -cf, -]}\nsrc:\n  <<: *m\n  files: *c\n"
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	section := doc.Content[0].Content[5]
	var got sample
	if err := Decode(section, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Files, Strings{"x", "y"}) || !slices.Equal(got.Cmd, Command{"tar", "-cf", "-"}) {
		t.Errorf("got %+v", got)
	}
}

func TestDecode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "dump.rdb")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		text    string
		want    sample
		wantErr string
	}{
		{name: "files string", text: "files: htdocs", want: sample{Files: Strings{"htdocs"}}},
		{name: "files number", text: "files: 42", want: sample{Files: Strings{"42"}}},
		{name: "files list", text: "files: [a, 7, c]", want: sample{Files: Strings{"a", "7", "c"}}},
		{name: "files null", text: "name: x\nfiles:", wantErr: "line 2: files has no value"},
		{name: "files alias", text: "x: &f [a, b]\nfiles: *f", wantErr: `unknown key "x"`},
		{name: "files bool", text: "files: true", wantErr: "want a string"},
		{name: "files mapping", text: "files: {a: b}", wantErr: "want a string, got a mapping"},
		{name: "files nested list", text: "files: [[a]]", wantErr: "want a string, got a list"},
		{name: "cmd list", text: "cmd: [pigz, -p, 4]", want: sample{Cmd: Command{"pigz", "-p", "4"}}},
		{name: "cmd string", text: "cmd: tar -cf -", wantErr: "a command must be a list"},
		{name: "cmd empty", text: "cmd: []", wantErr: "a command must not be empty"},
		{name: "cmd null", text: "cmd: ~", wantErr: "cmd has no value"},
		{name: "cmd mapping", text: "cmd: {a: b}", wantErr: "got a mapping"},
		{name: "cwd exists", text: "cwd: " + dir, want: sample{Cwd: Dir(dir)}},
		{name: "cwd missing", text: "cwd: " + dir + "/nope", wantErr: "does not exist"},
		{name: "cwd is a file", text: "cwd: " + file, wantErr: "is not a directory"},
		{name: "cwd empty", text: `cwd: ""`, wantErr: "empty path"},
		{name: "dump exists", text: "dump: " + file, want: sample{Dump: File(file)}},
		{name: "dump is a dir", text: "dump: " + dir, wantErr: "is not a regular file"},
		{name: "dump missing", text: "dump: " + dir + "/nope", wantErr: "does not exist"},
		{name: "unknown key", text: "name: x\nfiel: y", wantErr: `line 2: unknown key "fiel"`},
		{name: "nested unknown key", text: "mount: {point: /mnt, cmnd: [a]}", wantErr: `line 1: unknown key "cmnd"`},
		{name: "nested", text: "mount: {point: /mnt, cmd: [a]}", want: sample{Mount: mount{Point: "/mnt", Cmd: Command{"a"}}}},
		{name: "inline unknown key", text: "base: 1\nextra: 2", wantErr: `unknown key "extra"`},
		{name: "raw node keeps any key", text: "raw: {class: tar, anything: 1}", want: sample{}},
		{name: "merge", text: "<<: {name: x, files: a}\ncmd: [b]", want: sample{Files: Strings{"a"}, Cmd: Command{"b"}}},
		{name: "merge unknown key", text: "<<: {nme: x}", wantErr: `unknown key "nme"`},
		{name: "list of structs unknown key", text: "items: [{point: a}, {pont: b}]", wantErr: `unknown key "pont"`},
		{name: "map of structs unknown key", text: "byname: {a: {pont: b}}", wantErr: `unknown key "pont"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := decode(t, tt.text)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Files, tt.want.Files) || !slices.Equal(got.Cmd, tt.want.Cmd) ||
				got.Cwd != tt.want.Cwd || got.Dump != tt.want.Dump ||
				got.Mount.Point != tt.want.Mount.Point || !slices.Equal(got.Mount.Cmd, tt.want.Mount.Cmd) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
