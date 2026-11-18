// Package config loads bkp configs into profiles and holds the value types
// class configs are decoded with.
package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Decode decodes n into v, a pointer to a struct, failing on a key v has no
// field for and on a key without a value whose type checks its value (Strings,
// Command, Dir, File and the like). yaml.Node.Decode does not check keys, so
// they are checked here first.
func Decode(n *yaml.Node, v any) error {
	if v == nil {
		return errors.New("decode into nil")
	}
	if err := checkKeys(n, reflect.TypeOf(v)); err != nil {
		return err
	}
	return n.Decode(v)
}

var (
	unmarshalerType = reflect.TypeFor[yaml.Unmarshaler]()
	nodeType        = reflect.TypeFor[yaml.Node]()
)

func unmarshals(t reflect.Type) bool {
	return t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType)
}

// resolve follows aliases and unwraps a document.
func resolve(n *yaml.Node) *yaml.Node {
	for {
		switch {
		case n.Kind == yaml.AliasNode && n.Alias != nil:
			n = n.Alias
		case n.Kind == yaml.DocumentNode && len(n.Content) == 1:
			n = n.Content[0]
		default:
			return n
		}
	}
}

// checkKeys walks n as it would be decoded into t and reports the first
// mapping key t has no field for. Type mismatches are left to the decoder.
func checkKeys(n *yaml.Node, t reflect.Type) error {
	n = resolve(n)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// A yaml.Node field keeps its value raw, to be decoded later.
	if t == nodeType || unmarshals(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		fields, open := keysOf(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.ShortTag() == "!!merge" {
				if err := checkMerge(v, t); err != nil {
					return err
				}
				continue
			}
			ft, ok := fields[k.Value]
			if !ok {
				if open {
					continue
				}
				return fmt.Errorf("line %d: unknown key %q", k.Line, k.Value)
			}
			if unmarshals(ft) && resolve(v).ShortTag() == "!!null" {
				return fmt.Errorf("line %d: %s has no value", k.Line, k.Value)
			}
			if err := checkKeys(v, ft); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if n.Kind != yaml.SequenceNode {
			return nil
		}
		for _, item := range n.Content {
			if err := checkKeys(item, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 1; i < len(n.Content); i += 2 {
			if err := checkKeys(n.Content[i], t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMerge checks the mappings a merge key "<<" brings into a struct.
func checkMerge(n *yaml.Node, t reflect.Type) error {
	n = resolve(n)
	if n.Kind != yaml.SequenceNode {
		return checkKeys(n, t)
	}
	for _, item := range n.Content {
		if err := checkKeys(item, t); err != nil {
			return err
		}
	}
	return nil
}

// keysOf returns the mapping keys struct t decodes, with their types, and
// whether an inline map lets it take any other key.
func keysOf(t reflect.Type) (keys map[string]reflect.Type, open bool) {
	keys = map[string]reflect.Type{}
	for f := range t.Fields() {
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if strings.Contains(opts, "inline") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Map {
				open = true
				continue
			}
			inner, innerOpen := keysOf(ft)
			for k, v := range inner {
				keys[k] = v
			}
			open = open || innerOpen
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		keys[name] = f.Type
	}
	return keys, open
}

// scalar returns the text of a string or number scalar.
func scalar(n *yaml.Node) (string, error) {
	n = resolve(n)
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("line %d: want a string, got a %s", n.Line, kind(n))
	}
	switch n.ShortTag() {
	case "!!str", "!!int", "!!float":
		return n.Value, nil
	}
	return "", fmt.Errorf("line %d: want a string, got %s %q", n.Line, n.ShortTag(), n.Value)
}

func kind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.SequenceNode:
		return "list"
	case yaml.MappingNode:
		return "mapping"
	case yaml.AliasNode:
		return "alias"
	}
	return "scalar"
}

// given describes n for an error message.
func given(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return fmt.Sprintf("%q", n.Value)
	}
	return "a " + kind(n)
}

func scalars(n *yaml.Node) ([]string, error) {
	out := make([]string, 0, len(n.Content))
	for _, item := range resolve(n).Content {
		s, err := scalar(item)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Strings is a value written as one string or a list of them, such as
// files, exclude and dbs. A number is taken as its text.
//
// The YAML library does not call UnmarshalYAML for a key without a value, so
// Decode refuses such keys for this type and the others below; a field left
// out altogether stays empty and the class checks whether it is required.
type Strings []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *Strings) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		v, err := scalars(n)
		if err != nil {
			return err
		}
		*s = v
		return nil
	}
	v, err := scalar(n)
	if err != nil {
		return err
	}
	*s = Strings{v}
	return nil
}

// Command is an argv written as a YAML list, such as cmd. A string is
// refused: it would be one argv element with spaces inside, which never
// runs.
type Command []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *Command) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: a command must be a list, such as [tar, -cf, -], got %s", n.Line, given(n))
	}
	v, err := scalars(n)
	if err != nil {
		return err
	}
	if len(v) == 0 {
		return fmt.Errorf("line %d: a command must not be empty", n.Line)
	}
	*c = v
	return nil
}

// Dir is the path of a directory that exists when the config is loaded.
type Dir string

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Dir) UnmarshalYAML(n *yaml.Node) error {
	p, err := existing(n)
	if err != nil {
		return err
	}
	if !p.IsDir() {
		return fmt.Errorf("line %d: %s is not a directory", n.Line, n.Value)
	}
	*d = Dir(n.Value)
	return nil
}

// File is the path of a regular file that exists when the config is loaded.
type File string

// UnmarshalYAML implements yaml.Unmarshaler.
func (f *File) UnmarshalYAML(n *yaml.Node) error {
	p, err := existing(n)
	if err != nil {
		return err
	}
	if !p.Mode().IsRegular() {
		return fmt.Errorf("line %d: %s is not a regular file", n.Line, n.Value)
	}
	*f = File(n.Value)
	return nil
}

func existing(n *yaml.Node) (os.FileInfo, error) {
	path, err := scalar(n)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("line %d: empty path", n.Line)
	}
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("line %d: %s does not exist", n.Line, path)
	}
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", n.Line, err)
	}
	return fi, nil
}
