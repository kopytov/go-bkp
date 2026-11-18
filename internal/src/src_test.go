package src

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
)

// build constructs class from the YAML of its section without the class
// key, as config.Load hands it over.
func build(t *testing.T, class, text string) (config.Source, error) {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	n := &yaml.Node{Kind: yaml.MappingNode}
	if len(doc.Content) > 0 {
		n = doc.Content[0]
	}
	return Classes[class](n)
}

func TestArgv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	defaults := filepath.Join(dir, "my.cnf")
	if err := os.WriteFile(defaults, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tarCmd := []string{"tar", "--warning=none", "--numeric-owner", "-cf", "-"}
	dump := []string{"mysqldump", "-ER", "--single-transaction"}
	tests := []struct {
		name, class, text string
		argv              []string
		allow             []int
		suffix            string
	}{
		{
			name: "cmd", class: "cmd", text: "cmd: [zfs, send, pool@snap]\nsuffix: zfs",
			argv: []string{"zfs", "send", "pool@snap"}, suffix: "zfs",
		},
		{
			name: "tar defaults", class: "tar", text: "files: htdocs",
			argv: append(slices.Clone(tarCmd), "htdocs"), allow: []int{1}, suffix: "tar",
		},
		{
			name: "tar all attributes", class: "tar",
			text:  "cwd: " + dir + "\nfiles: [a, b]\nexclude: [a/cache, '*.log']\nsuffix: tarball",
			argv:  append(slices.Clone(tarCmd), "-C", dir, "--exclude=a/cache", "--exclude=*.log", "a", "b"),
			allow: []int{1}, suffix: "tarball",
		},
		{
			name: "tar cmd", class: "tar", text: "cmd: [tar, --one-file-system, -cf, -]\nexclude: x\nfiles: a",
			argv: []string{"tar", "--one-file-system", "-cf", "-", "--exclude=x", "a"}, allow: []int{1}, suffix: "tar",
		},
		{
			name: "mysql defaults", class: "mysql", text: "{}",
			argv: append(slices.Clone(dump), "--all-databases"), suffix: "sql",
		},
		{
			name: "mysql all attributes", class: "mysql", text: "dbs: [db1, db2]\ndefaults-file: " + defaults,
			argv: append(slices.Clone(dump), "--defaults-file="+defaults, "db1", "db2"), suffix: "sql",
		},
		{
			name: "mysql one db", class: "mysql", text: "dbs: wordpress",
			argv: append(slices.Clone(dump), "wordpress"), suffix: "sql",
		},
		{
			name: "mysql cmd", class: "mysql", text: "cmd: [docker, exec, mysql, mysqldump]",
			argv: []string{"docker", "exec", "mysql", "mysqldump", "--all-databases"}, suffix: "sql",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, err := build(t, tt.class, tt.text)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Suffix(); got != tt.suffix {
				t.Errorf("suffix %q, want %q", got, tt.suffix)
			}
			spec, err := s.Spec()
			if err != nil {
				t.Fatal(err)
			}
			if spec.In != nil || len(spec.Stages) != 1 {
				t.Fatalf("spec %+v, want one stage", spec)
			}
			st := spec.Stages[0]
			if st.Name != "src "+tt.class {
				t.Errorf("name %q", st.Name)
			}
			if !slices.Equal(st.Argv, tt.argv) {
				t.Errorf("argv %q\nwant %q", st.Argv, tt.argv)
			}
			if !slices.Equal(st.Allow, tt.allow) {
				t.Errorf("allow %v, want %v", st.Allow, tt.allow)
			}
		})
	}
}

// TestArgvFresh checks that building argv leaves the class as it was, so
// the next attempt gets the same command.
func TestArgvFresh(t *testing.T) {
	t.Parallel()
	s, err := build(t, "tar", "cmd: [tar, -cf, -]\nfiles: a")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Spec()
	if err != nil {
		t.Fatal(err)
	}
	first.Stages[0].Argv[0] = "changed"
	second, err := s.Spec()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"tar", "-cf", "-", "a"}; !slices.Equal(second.Stages[0].Argv, want) {
		t.Errorf("second argv %q, want %q", second.Stages[0].Argv, want)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tests := []struct{ name, class, text, wantErr string }{
		{"cmd without cmd", "cmd", "suffix: zfs", "cmd is required"},
		{"cmd without suffix", "cmd", "cmd: [zfs, send]", "suffix is required"},
		{"cmd as a string", "cmd", "cmd: zfs send\nsuffix: zfs", "a command must be a list"},
		{"cmd empty suffix", "cmd", "cmd: [a]\nsuffix: ''", `suffix "" must not be empty`},
		{"cmd suffix with a slash", "cmd", "cmd: [a]\nsuffix: ../x", "must not be empty, start with a dot or contain a slash"},
		{"tar without files", "tar", "cwd: " + dir, "files is required"},
		{"tar empty files", "tar", "files: []", "files is required and must not be empty"},
		{"tar files null", "tar", "files: ~", "files has no value"},
		{"tar missing cwd", "tar", "cwd: " + dir + "/nope\nfiles: a", "does not exist"},
		{"tar unknown key", "tar", "files: a\nexclud: b", `unknown key "exclud"`},
		{"mysql empty dbs", "mysql", "dbs: []", "dbs must not be empty"},
		{"mysql missing defaults-file", "mysql", "defaults-file: " + dir + "/my.cnf", "does not exist"},
		{"mysql defaults-file a dir", "mysql", "defaults-file: " + dir, "is not a regular file"},
		{"redis without dump", "redis", "{}", "dump is required"},
		{"redis missing dump", "redis", "dump: " + dir + "/dump.rdb", "does not exist"},
		{"redis cmd", "redis", "dump: " + dir + "\ncmd: [cat]", `unknown key "cmd"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := build(t, tt.class, tt.text)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRedis(t *testing.T) {
	t.Parallel()
	dump := filepath.Join(t.TempDir(), "dump.rdb")
	if err := os.WriteFile(dump, []byte("REDIS0011"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := build(t, "redis", "dump: "+dump)
	if err != nil {
		t.Fatal(err)
	}
	if s.Suffix() != "rdb" {
		t.Errorf("suffix %q", s.Suffix())
	}
	spec, err := s.Spec()
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Stages) != 0 || spec.In == nil {
		t.Fatalf("spec %+v, want the dump as input and no stages", spec)
	}
	defer spec.Close()
	data, err := io.ReadAll(spec.In)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "REDIS0011" {
		t.Errorf("read %q", data)
	}

	// The dump is checked at load, but may be gone by the attempt.
	if err := os.Remove(dump); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Spec(); err == nil || !strings.Contains(err.Error(), "open dump: ") {
		t.Errorf("err = %v, want open dump", err)
	}
}
