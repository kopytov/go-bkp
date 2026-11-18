package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Profile is one archive a config makes: a source, the optional filters in
// the order the data passes them, a destination and a post hook.
type Profile struct {
	Name     string
	Src      Source
	Compress Filter // nil without a compress section
	Crypt    Filter // nil without a crypt section
	Shape    Filter // nil without a shape section
	Dst      Dest
	// Post is a shell command run after the archive is written, or empty.
	Post string
}

// Suffix returns the suffix of the profile's archives, such as "tar.gz.enc".
func (p Profile) Suffix() string {
	s := p.Src.Suffix()
	for _, f := range []Filter{p.Compress, p.Crypt, p.Shape} {
		if f != nil {
			s = f.Suffix(s)
		}
	}
	return s
}

// Spec returns the pipeline of one attempt without its output: the source
// and then the filters in the order the data passes them. On an error it
// closes what the classes opened so far.
func (p Profile) Spec() (pipeline.Spec, error) {
	s, err := p.Src.Spec()
	if err != nil {
		return pipeline.Spec{}, fmt.Errorf("src: %w", err)
	}
	for _, f := range []struct {
		section string
		filter  Filter
	}{
		{"compress", p.Compress},
		{"crypt", p.Crypt},
		{"shape", p.Shape},
	} {
		if f.filter == nil {
			continue
		}
		st, err := f.filter.Stage()
		if err != nil {
			s.Close()
			return pipeline.Spec{}, fmt.Errorf("%s: %w", f.section, err)
		}
		s.Stages = append(s.Stages, st)
	}
	return s, nil
}

// sections are the keys allowed at the top of a config.
var sections = []string{"src", "compress", "crypt", "shape", "dst", "post"}

// Load reads the config at path and builds its profiles, sorted by name,
// with the classes of classes. Any error in the config is returned here,
// before any stage runs; only a src.script is run, to list the sources.
//
// A src with a class key is the one source of the config; the profile is
// named after the file and its dst.prefix defaults to that name. Otherwise
// src maps names to sources, and each makes a profile of that name with its
// own copy of the other sections and that name as dst.prefix. A src with a
// script key gets that mapping from its script and template; see expand.
func Load(ctx context.Context, path string, classes Classes) ([]Profile, error) {
	ps, err := load(ctx, path, classes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ps, nil
}

func load(ctx context.Context, path string, classes Classes) ([]Profile, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: reading the config the operator named is the point
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("no such file")
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("config is empty")
	}
	root, err := flatten(doc.Content[0])
	if err != nil {
		return nil, err
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: want a mapping of sections, got %s", root.Line, given(root))
	}
	for i := 0; i < len(root.Content); i += 2 {
		k := root.Content[i]
		if !slices.Contains(sections, k.Value) {
			return nil, fmt.Errorf("line %d: unknown section %q, want one of %s",
				k.Line, k.Value, strings.Join(sections, ", "))
		}
	}
	src := find(root, "src")
	if src == nil {
		return nil, errors.New("src is required")
	}
	if find(root, "dst") == nil {
		return nil, errors.New("dst is required")
	}
	if src.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: src: want a mapping, got %s", src.Line, given(src))
	}

	if has(src, "script") {
		if src, err = expand(ctx, src); err != nil {
			return nil, fmt.Errorf("src: %w", err)
		}
	} else if find(src, "class") != nil {
		if prefix := find(find(root, "dst"), "prefix"); prefix != nil {
			v, err := scalar(prefix)
			if err != nil {
				return nil, fmt.Errorf("dst.prefix: %w", err)
			}
			if err := checkName(v); err != nil {
				return nil, fmt.Errorf("line %d: dst.prefix: %w", prefix.Line, err)
			}
		}
		name := profileName(path)
		p, err := profile(name, src, root, classes, false)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", name, err)
		}
		return []Profile{p}, nil
	}

	if len(src.Content) == 0 {
		return nil, fmt.Errorf("line %d: src has no sources", src.Line)
	}
	if prefix := find(find(root, "dst"), "prefix"); prefix != nil {
		return nil, fmt.Errorf("line %d: dst.prefix is not allowed with several sources: "+
			"each profile takes the name of its source", prefix.Line)
	}
	var ps []Profile
	for i := 0; i < len(src.Content); i += 2 {
		k, v := src.Content[i], src.Content[i+1]
		name, err := scalar(k)
		if err != nil {
			return nil, fmt.Errorf("src: source name: %w", err)
		}
		if err := checkName(name); err != nil {
			return nil, fmt.Errorf("line %d: src: source name: %w", k.Line, err)
		}
		p, err := profile(name, v, root, classes, true)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", name, err)
		}
		ps = append(ps, p)
	}
	slices.SortFunc(ps, func(a, b Profile) int { return strings.Compare(a.Name, b.Name) })
	return ps, nil
}

// checkName checks a source name or prefix, which starts archive names: it
// must not be empty or lead out of the destination directory.
func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("%q cannot start an archive name: it must not be empty, . or .. "+
			"or contain a slash", name)
	}
	return nil
}

// profileName names the profile of a single-source config after its file:
// the base name without .yml or .yaml in any case, or, without that
// extension, the base name in lower case.
func profileName(path string) string {
	base := filepath.Base(path)
	for _, ext := range []string{".yml", ".yaml"} {
		if len(base) > len(ext) && strings.EqualFold(base[len(base)-len(ext):], ext) {
			return base[:len(base)-len(ext)]
		}
	}
	return strings.ToLower(base)
}

// profile builds the profile name from source src and the other sections of
// root. Every section is copied, so profiles share no YAML nodes. dst.prefix
// is set to name: always with setPrefix, otherwise only when it is not given.
func profile(name string, src, root *yaml.Node, classes Classes, setPrefix bool) (Profile, error) {
	p := Profile{Name: name}
	var err error
	if p.Src, err = build(classes.Src, copyNode(src)); err != nil {
		return Profile{}, fmt.Errorf("src: %w", err)
	}
	for _, f := range []struct {
		section string
		table   Table[Filter]
		out     *Filter
	}{
		{"compress", classes.Compress, &p.Compress},
		{"crypt", classes.Crypt, &p.Crypt},
		{"shape", classes.Shape, &p.Shape},
	} {
		n := find(root, f.section)
		if n == nil || blank(n) {
			continue
		}
		if *f.out, err = build(f.table, copyNode(n)); err != nil {
			return Profile{}, fmt.Errorf("%s: %w", f.section, err)
		}
	}

	dst := copyNode(find(root, "dst"))
	if dst.Kind == yaml.MappingNode && (setPrefix || find(dst, "prefix") == nil) {
		take(dst, "prefix")
		dst.Content = append(dst.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "prefix", Line: dst.Line},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name, Line: dst.Line})
	}
	if p.Dst, err = build(classes.Dst, dst); err != nil {
		return Profile{}, fmt.Errorf("dst: %w", err)
	}

	if post := find(root, "post"); post != nil {
		if post.Kind != yaml.ScalarNode || post.ShortTag() != "!!str" {
			return Profile{}, fmt.Errorf("line %d: post: want a shell command as a string, got %s",
				post.Line, given(post))
		}
		p.Post = post.Value
	}
	return p, nil
}
