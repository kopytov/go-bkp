package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// fake is a stand-in class: it keeps its class name and attributes, so tests
// can see what each profile got without the real classes.
type fake struct {
	class string
	attrs map[string]any
}

type fakeSrc struct{ fake }

func (f *fakeSrc) Suffix() string { return f.class }

type fakeFilter struct{ fake }

func (f *fakeFilter) Suffix(in string) string { return in + "." + f.class }

type fakeDst struct{ fake }

func (f *fakeDst) Prefix() string {
	p, _ := f.attrs["prefix"].(string)
	return p
}

func newFake(class string, n *yaml.Node) (fake, error) {
	f := fake{class: class}
	err := n.Decode(&f.attrs)
	return f, err
}

func table[T any](wrap func(fake) T, names ...string) Table[T] {
	t := Table[T]{}
	for _, name := range names {
		t[name] = func(n *yaml.Node) (T, error) {
			f, err := newFake(name, n)
			return wrap(f), err
		}
	}
	return t
}

// strictSrc is a class with checked attributes, to see their errors come
// out of Load.
type strictSrc struct {
	Files Strings `yaml:"files"`
	Cmd   Command `yaml:"cmd"`
}

func (*strictSrc) Suffix() string { return "strict" }

func testClasses() Classes {
	filter := func(f fake) Filter { return &fakeFilter{f} }
	c := Classes{
		Src:      table(func(f fake) Source { return &fakeSrc{f} }, "tar", "mysql", "redis", "cmd"),
		Compress: table(filter, "gzip", "pigz"),
		Crypt:    table(filter, "openssl"),
		Shape:    table(filter, "pv"),
		Dst:      table(func(f fake) Dest { return &fakeDst{f} }, "local", "ftp"),
	}
	c.Src["strict"] = func(n *yaml.Node) (Source, error) {
		var s strictSrc
		err := Decode(n, &s)
		return &s, err
	}
	return c
}

// summary is what a test checks of a profile.
type summary struct {
	name, src, compress, crypt, shape, dst, prefix, suffix string
}

func class(v any) string {
	switch f := v.(type) {
	case *fakeSrc:
		return f.class
	case *fakeFilter:
		if f != nil {
			return f.class
		}
	case *fakeDst:
		return f.class
	case *strictSrc:
		return "strict"
	}
	return ""
}

func filterClass(f Filter) string {
	if f == nil {
		return ""
	}
	return class(f)
}

func summarize(ps []Profile) []summary {
	var out []summary
	for _, p := range ps {
		out = append(out, summary{
			name: p.Name, src: class(p.Src),
			compress: filterClass(p.Compress), crypt: filterClass(p.Crypt), shape: filterClass(p.Shape),
			dst: class(p.Dst), prefix: p.Dst.Prefix(), suffix: p.Suffix(),
		})
	}
	return out
}

func checkLoad(t *testing.T, path string, want []summary, wantErr string) {
	t.Helper()
	ps, err := Load(t.Context(), path, testClasses())
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("err = %v, want it to contain %q", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	got := summarize(ps)
	if len(got) != len(want) {
		t.Fatalf("got %d profiles %+v, want %+v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("profile %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestLoadREADME loads the config examples of the Raku README. Sections an
// example lacks are filled in with the simplest ones; see the comment at
// the top of each file. The real classes add their checks to these
// examples as they are ported.
func TestLoadREADME(t *testing.T) {
	t.Parallel()
	tar := func(name string) summary {
		return summary{name: name, src: "tar", dst: "local", prefix: name, suffix: "tar"}
	}
	tests := []struct {
		file    string
		want    []summary
		wantErr string
	}{
		{file: "readme-full.yml", want: []summary{
			{name: "database", src: "mysql", compress: "gzip", dst: "ftp", prefix: "database", suffix: "mysql.gzip"},
			{name: "files", src: "tar", compress: "gzip", dst: "ftp", prefix: "files", suffix: "tar.gzip"},
		}},
		{file: "readme-src-single.yml", want: []summary{tar("readme-src-single")}},
		{file: "readme-src-multi.yml", want: []summary{
			{name: "database", src: "mysql", dst: "local", prefix: "database", suffix: "mysql"},
			tar("site"),
		}},
		{file: "readme-tar.yml", want: []summary{tar("readme-tar")}},
		{file: "readme-mysql.yml", want: []summary{
			{name: "readme-mysql", src: "mysql", dst: "local", prefix: "readme-mysql", suffix: "mysql"},
		}},
		{file: "readme-mysql-split.yml", want: []summary{
			{name: "db1", src: "mysql", dst: "local", prefix: "db1", suffix: "mysql"},
			{name: "db2", src: "mysql", dst: "local", prefix: "db2", suffix: "mysql"},
		}},
		{file: "readme-redis.yml", want: []summary{
			{name: "readme-redis", src: "redis", dst: "local", prefix: "readme-redis", suffix: "redis"},
		}},
		{file: "readme-gzip.yml", want: []summary{{
			name: "readme-gzip", src: "tar", compress: "gzip", dst: "local", prefix: "readme-gzip", suffix: "tar.gzip",
		}}},
		{file: "readme-pigz.yml", want: []summary{{
			name: "readme-pigz", src: "tar", compress: "pigz", dst: "local", prefix: "readme-pigz", suffix: "tar.pigz",
		}}},
		{file: "readme-openssl.yml", want: []summary{{
			name: "readme-openssl", src: "tar", crypt: "openssl", dst: "local", prefix: "readme-openssl",
			suffix: "tar.openssl",
		}}},
		{file: "readme-pv.yml", want: []summary{{
			name: "readme-pv", src: "tar", shape: "pv", dst: "local", prefix: "readme-pv", suffix: "tar.pv",
		}}},
		{file: "readme-plan.yml", want: []summary{tar("readme-plan")}},
		{file: "readme-local.yml", want: []summary{tar("readme-local")}},
		{file: "readme-ftp.yml", want: []summary{{
			name: "readme-ftp", src: "tar", dst: "ftp", prefix: "readme-ftp", suffix: "tar",
		}}},
		{file: "readme-ploop.yml", wantErr: `src: line 4: unknown class "ploop"`},
		{file: "readme-lvm-device.yml", wantErr: `unknown class "lvm"`},
		{file: "readme-lvm-vm.yml", wantErr: `unknown class "lvm"`},
		{file: "readme-virsh.yml", wantErr: "line 5: mapping values are not allowed"},
		{file: "readme-virsh-fixed.yml", wantErr: `unknown class "virsh"`},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()
			checkLoad(t, filepath.Join("testdata", tt.file), tt.want, tt.wantErr)
		})
	}
}

func writeConfig(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const dstLocal = "dst:\n  class: local\n  dir: /srv\n"

func TestLoad(t *testing.T) {
	t.Parallel()
	one := "src:\n  class: tar\n  files: a\n"
	tests := []struct {
		name    string
		file    string
		text    string
		want    []summary
		wantErr string
	}{
		{name: "name from .yml", file: "www.yml", text: one + dstLocal, want: []summary{tar("www", "www")}},
		{name: "name from .yaml", file: "www.yaml", text: one + dstLocal, want: []summary{tar("www", "www")}},
		{name: "extension in upper case", file: "WWW.YML", text: one + dstLocal, want: []summary{tar("WWW", "WWW")}},
		{name: "no extension", file: "WWW", text: one + dstLocal, want: []summary{tar("www", "www")}},
		{name: "only an extension", file: ".yml", text: one + dstLocal, want: []summary{tar(".yml", ".yml")}},
		{name: "other extension", file: "www.conf", text: one + dstLocal, want: []summary{tar("www.conf", "www.conf")}},
		{
			name: "explicit prefix", file: "www.yml", text: one + dstLocal + "  prefix: site\n",
			want: []summary{tar("www", "site")},
		},
		{
			name: "several sources sorted", file: "x.yml",
			text: "src:\n  b: {class: tar}\n  a: {class: tar}\n  C: {class: tar}\n" + dstLocal,
			want: []summary{tar("C", "C"), tar("a", "a"), tar("b", "b")},
		},
		{
			name: "prefix with several sources", file: "x.yml",
			text:    "src:\n  a: {class: tar}\n" + dstLocal + "  prefix: site\n",
			wantErr: "line 6: dst.prefix is not allowed with several sources",
		},
		{
			name: "class in another case", file: "x.yml",
			text: "src:\n  class: TaR\ncompress:\n  class: GZip\n" + dstLocal + "post: echo done\n",
			want: []summary{{name: "x", src: "tar", compress: "gzip", dst: "local", prefix: "x", suffix: "tar.gzip"}},
		},
		{
			name: "empty optional sections", file: "x.yml",
			text: one + "compress:\ncrypt: ~\n" + dstLocal, want: []summary{tar("x", "x")},
		},
		{
			name: "anchors and merge keys", file: "x.yml",
			text: "src:\n  a: &a {class: tar, files: x}\n  b:\n    <<: *a\n    class: mysql\n" + dstLocal,
			want: []summary{tar("a", "a"), {name: "b", src: "mysql", dst: "local", prefix: "b", suffix: "mysql"}},
		},
		{
			name: "blank optional sections", file: "x.yml",
			text: one + "compress: {}\ncrypt: ''\nshape: 0.0\n" + dstLocal, want: []summary{tar("x", "x")},
		},
		{
			name: "null prefix with several sources", file: "x.yml",
			text: "src:\n  a: {class: tar}\n" + dstLocal + "  prefix: ~\n", want: []summary{tar("a", "a")},
		},
		{
			name: "merged prefix with several sources", file: "x.yml",
			text:    "src:\n  a: {class: tar, x: &b {prefix: site}}\ndst:\n  <<: *b\n  class: local\n",
			wantErr: "dst.prefix is not allowed with several sources",
		},
		{name: "source name with a slash", file: "x.yml", text: "src:\n  a/b: {class: tar}\n" + dstLocal, wantErr: `line 2: src: source name: "a/b" cannot start an archive name`},
		{name: "source name ..", file: "x.yml", text: "src:\n  ..: {class: tar}\n" + dstLocal, wantErr: `".." cannot start an archive name`},
		{name: "source name empty", file: "x.yml", text: "src:\n  '': {class: tar}\n" + dstLocal, wantErr: `"" cannot start an archive name`},
		{name: "prefix with a slash", file: "x.yml", text: one + dstLocal + "  prefix: ../x\n", wantErr: `line 7: dst.prefix: "../x" cannot start an archive name`},
		{name: "alias to itself", file: "x.yml", text: "src: &x {class: tar, a: *x}\n" + dstLocal, wantErr: "alias refers to the node it is in"},
		{name: "alias growing", file: "x.yml", text: laughs + "src:\n  class: tar\n  x: *h\n" + dstLocal, wantErr: "more than 100000 nodes"},
		{name: "missing file", file: "", wantErr: "nope.yml: no such file"},
		{name: "unknown section", file: "x.yml", text: one + dstLocal + "plan: {d: 1}\n", wantErr: `line 7: unknown section "plan"`},
		{name: "unknown src class", file: "x.yml", text: "src:\n  class: rsync\n" + dstLocal, wantErr: `x.yml: profile x: src: line 2: unknown class "rsync", known: cmd, mysql, redis, strict, tar`},
		{name: "unknown dst class", file: "x.yml", text: one + "dst:\n  class: s3\n", wantErr: `profile x: dst: line 5: unknown class "s3", known: ftp, local`},
		{name: "unknown class among several", file: "x.yml", text: "src:\n  a: {class: tar}\n  b: {class: lvm}\n" + dstLocal, wantErr: `profile b: src: line 3: unknown class "lvm"`},
		{name: "no class", file: "x.yml", text: one + "compress:\n  level: 9\n" + dstLocal, wantErr: "compress: line 5: class is required"},
		{name: "empty class", file: "x.yml", text: "src:\n  class: ''\n" + dstLocal, wantErr: "class is empty"},
		{name: "class not a string", file: "x.yml", text: "src:\n  class: [tar]\n" + dstLocal, wantErr: "class: line 2: want a string"},
		{name: "section not a mapping", file: "x.yml", text: one + "compress: gzip\n" + dstLocal, wantErr: "compress: line 4: want a mapping with a class"},
		{name: "source not a mapping", file: "x.yml", text: "src:\n  a: tar\n" + dstLocal, wantErr: "profile a: src: line 2: want a mapping"},
		{name: "no sources", file: "x.yml", text: "src: {}\n" + dstLocal, wantErr: "src has no sources"},
		{name: "src a list", file: "x.yml", text: "src: [a]\n" + dstLocal, wantErr: "src: want a mapping, got a list"},
		{name: "no src", file: "x.yml", text: dstLocal, wantErr: "src is required"},
		{name: "no dst", file: "x.yml", text: one, wantErr: "dst is required"},
		{name: "empty file", file: "x.yml", text: "", wantErr: "config is empty"},
		{name: "not a mapping", file: "x.yml", text: "- src\n", wantErr: "want a mapping of sections"},
		{name: "duplicate section", file: "x.yml", text: one + dstLocal + dstLocal, wantErr: `key "dst" given twice`},
		{name: "duplicate source", file: "x.yml", text: "src:\n  a: {class: tar}\n  a: {class: tar}\n" + dstLocal, wantErr: `key "a" given twice`},
		{name: "post a list", file: "x.yml", text: one + dstLocal + "post: [echo]\n", wantErr: "post: want a shell command as a string"},
		{name: "attribute error", file: "x.yml", text: "src:\n  class: strict\n  cmd: tar -cf -\n" + dstLocal, wantErr: "profile x: src: class strict: line 3: a command must be a list"},
		{name: "unknown attribute", file: "x.yml", text: "src:\n  class: strict\n  fils: a\n" + dstLocal, wantErr: `profile x: src: class strict: line 3: unknown key "fils"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "nope.yml")
			if tt.file != "" {
				path = writeConfig(t, tt.file, tt.text)
			}
			checkLoad(t, path, tt.want, tt.wantErr)
		})
	}
}

// laughs is a chain of aliases, each doubling the one before, that expands
// to 2^20 nodes: a config made to exhaust memory.
var laughs = func() string {
	var b strings.Builder
	b.WriteString("x: &a0 [a, a]\n")
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "x%d: &a%d [*a%d, *a%d]\n", i, i, i-1, i-1)
	}
	return b.String() + "z: &h [*a20]\n"
}()

func tar(name, prefix string) summary {
	return summary{name: name, src: "tar", dst: "local", prefix: prefix, suffix: "tar"}
}

func TestLoadPost(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "x.yml", "src:\n  a: {class: tar}\n  b: {class: tar}\n"+dstLocal+"post: systemctl reload x\n")
	ps, err := Load(t.Context(), path, testClasses())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Post != "systemctl reload x" {
			t.Errorf("profile %s: post = %q", p.Name, p.Post)
		}
	}
}

// TestLoadIndependentProfiles checks that the profiles of one config share
// no state: each got its own copy of every section.
func TestLoadIndependentProfiles(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "x.yml", "src:\n  a: {class: tar}\n  b: {class: tar}\n"+
		"compress: &c {class: gzip, level: 9}\ncrypt: {<<: *c, class: openssl}\n"+dstLocal+"  plan: {d: 7}\n")
	ps, err := Load(t.Context(), path, testClasses())
	if err != nil {
		t.Fatal(err)
	}
	a, b := ps[0], ps[1]
	a.Dst.(*fakeDst).attrs["dir"] = "/changed"
	a.Dst.(*fakeDst).attrs["plan"].(map[string]any)["d"] = 1
	a.Compress.(*fakeFilter).attrs["level"] = 1
	if got := b.Dst.(*fakeDst).attrs["dir"]; got != "/srv" {
		t.Errorf("b dst dir = %v", got)
	}
	if got := b.Dst.(*fakeDst).attrs["plan"].(map[string]any)["d"]; got != 7 {
		t.Errorf("b dst plan.d = %v", got)
	}
	if got := b.Compress.(*fakeFilter).attrs["level"]; got != 9 {
		t.Errorf("b compress level = %v", got)
	}
	if got := b.Crypt.(*fakeFilter).attrs["level"]; got != 9 {
		t.Errorf("b crypt level = %v, want 9 merged from compress", got)
	}
	if _, ok := b.Crypt.(*fakeFilter).attrs["class"]; ok {
		t.Error("crypt got its class key")
	}
	if a.Dst.Prefix() != "a" || b.Dst.Prefix() != "b" {
		t.Errorf("prefixes %q, %q", a.Dst.Prefix(), b.Dst.Prefix())
	}
}
