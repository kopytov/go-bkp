package compress

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
	"github.com/kopytov/go-bkp/internal/src"
)

// section parses the YAML of a config section without its class key.
func section(t *testing.T, text string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	if len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}
	}
	return doc.Content[0]
}

func TestStage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, class, text string
		argv              []string
		allow             []int
		suffix            string
	}{
		{name: "gzip defaults", class: "gzip", text: "{}", argv: []string{"gzip", "-c"}, allow: []int{2}, suffix: "tar.gz"},
		{
			name: "gzip all attributes", class: "gzip", text: "level: 9\nadd-suffix: gzip",
			argv: []string{"gzip", "-c", "-9"}, allow: []int{2}, suffix: "tar.gzip",
		},
		{
			name: "gzip cmd", class: "gzip", text: "cmd: [nice, gzip, -c]\nlevel: 1",
			argv: []string{"nice", "gzip", "-c", "-1"}, allow: []int{2}, suffix: "tar.gz",
		},
		{name: "pigz defaults", class: "pigz", text: "{}", argv: []string{"pigz", "-c"}, suffix: "tar.gz"},
		{
			name: "pigz all attributes", class: "pigz", text: "level: 6\nprocs: 4\nadd-suffix: pgz",
			argv: []string{"pigz", "-c", "-6", "-p", "4"}, suffix: "tar.pgz",
		},
		{name: "pigz procs 0", class: "pigz", text: "procs: 0", argv: []string{"pigz", "-c"}, suffix: "tar.gz"},
		{
			name: "pigz cmd", class: "pigz", text: "cmd: [pigz, -c, --rsyncable]\nprocs: 2",
			argv: []string{"pigz", "-c", "--rsyncable", "-p", "2"}, suffix: "tar.gz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := Classes[tt.class](section(t, tt.text))
			if err != nil {
				t.Fatal(err)
			}
			if got := f.Suffix("tar"); got != tt.suffix {
				t.Errorf("suffix %q, want %q", got, tt.suffix)
			}
			st, err := f.Stage()
			if err != nil {
				t.Fatal(err)
			}
			if st.Name != "compress "+tt.class {
				t.Errorf("name %q", st.Name)
			}
			if !slices.Equal(st.Argv, tt.argv) {
				t.Errorf("argv %q, want %q", st.Argv, tt.argv)
			}
			if !slices.Equal(st.Allow, tt.allow) {
				t.Errorf("allow %v, want %v", st.Allow, tt.allow)
			}
			if len(st.Files) != 0 {
				t.Errorf("files %v", st.Files)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, class, text, wantErr string }{
		{"gzip level 0", "gzip", "level: 0", "line 1: level 0 is out of range 1-9"},
		{"gzip level 10", "gzip", "level: 10", "level 10 is out of range"},
		{"gzip level negative", "gzip", "level: -1", "level -1 is out of range"},
		{"gzip level string", "gzip", "level: best", "cannot unmarshal"},
		{"gzip level null", "gzip", "level: ~", "level has no value"},
		{"gzip empty add-suffix", "gzip", "add-suffix: ''", `suffix "" must not be empty`},
		{"gzip cmd a string", "gzip", "cmd: gzip -c", "a command must be a list"},
		{"gzip procs", "gzip", "procs: 2", `unknown key "procs"`},
		{"pigz level 11", "pigz", "level: 11", "level 11 is out of range"},
		{"pigz procs negative", "pigz", "procs: -2", "procs -2 must not be negative"},
		{"pigz procs null", "pigz", "procs: ~", "procs has no value"},
		{"pigz procs list", "pigz", "procs: [2]", "cannot unmarshal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Classes[tt.class](section(t, tt.text))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSuffixChain(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ src, srcText, want string }{
		{"tar", "files: a", "tar.gz"},
		{"mysql", "dbs: db1", "sql.gz"},
	} {
		s, err := src.Classes[tt.src](section(t, tt.srcText))
		if err != nil {
			t.Fatal(err)
		}
		gz, err := NewGzip(section(t, "{}"))
		if err != nil {
			t.Fatal(err)
		}
		p := config.Profile{Src: s, Compress: gz}
		if got := p.Suffix(); got != tt.want {
			t.Errorf("%s: suffix %q, want %q", tt.src, got, tt.want)
		}
	}
}

// TestGzipTar packs a directory with the real tar and gzip and unpacks the
// archive again.
func TestGzipTar(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"tar", "gzip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
	}
	root := t.TempDir()
	in := filepath.Join(root, "in")
	files := map[string][]byte{
		"site/index.html":    []byte("<html></html>\n"),
		"site/data.bin":      bytes.Repeat([]byte{0, 1, 2, 3, 255}, 100_000),
		"site/cache/skip.me": []byte("excluded\n"),
	}
	for name, data := range files {
		path := filepath.Join(in, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tar, err := src.Classes["tar"](section(t, "cwd: "+in+"\nfiles: site\nexclude: site/cache"))
	if err != nil {
		t.Fatal(err)
	}
	gz, err := NewGzip(section(t, "level: 1"))
	if err != nil {
		t.Fatal(err)
	}
	p := config.Profile{Src: tar, Compress: gz}
	spec, err := p.Spec()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "x."+p.Suffix())
	out, err := os.Create(archive) //nolint:gosec // G304: a path under t.TempDir
	if err != nil {
		spec.Close()
		t.Fatal(err)
	}
	defer out.Close() //nolint:errcheck // closed and checked below; this one is for a failed test
	spec.Out = out
	pl, err := pipeline.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := pl.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	unpacked := filepath.Join(root, "out")
	if err := os.Mkdir(unpacked, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "tar", "-xzf", archive, "-C", unpacked)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unpack: %v: %s", err, b)
	}
	for name, data := range files {
		got, err := os.ReadFile(filepath.Join(unpacked, name)) //nolint:gosec // G304: a path under t.TempDir
		if strings.HasPrefix(name, "site/cache/") {
			if !os.IsNotExist(err) {
				t.Errorf("%s: excluded file unpacked, err = %v", name, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("%s: contents differ after the round trip", name)
		}
	}
}
