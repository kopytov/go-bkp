package config

import (
	"fmt"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// maxNodes bounds the size of a config with its aliases expanded, so that
// aliases nested to grow exponentially fail the load instead of the host.
const maxNodes = 100_000

// flattener expands aliases. It tracks the alias targets being expanded, so
// an alias inside the node it refers to is an error rather than endless
// recursion.
type flattener struct {
	open  map[*yaml.Node]bool
	count int
}

// flatten returns a deep copy of n with aliases replaced by copies of what
// they point to and merge keys ("<<") replaced by the keys they bring in,
// so that sections can be looked into, changed and handed to each profile
// separately. A key given twice in one mapping is an error. Line numbers are
// kept.
func flatten(n *yaml.Node) (*yaml.Node, error) {
	f := flattener{open: map[*yaml.Node]bool{}}
	return f.flatten(n)
}

func (f *flattener) flatten(n *yaml.Node) (*yaml.Node, error) {
	line := n.Line
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if f.open[n] {
		return nil, fmt.Errorf("line %d: alias refers to the node it is in", line)
	}
	f.count++
	if f.count > maxNodes {
		return nil, fmt.Errorf("line %d: config has more than %d nodes with aliases expanded", line, maxNodes)
	}
	f.open[n] = true
	defer delete(f.open, n)
	c := *n
	c.Anchor = ""
	c.Content = nil
	switch n.Kind {
	case yaml.MappingNode:
		seen := map[string]bool{}
		var merged []*yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.ShortTag() == "!!merge" {
				m, err := f.mergeContent(v)
				if err != nil {
					return nil, err
				}
				merged = append(merged, m...)
				continue
			}
			if seen[k.Value] {
				return nil, fmt.Errorf("line %d: key %q given twice", k.Line, k.Value)
			}
			seen[k.Value] = true
			fk, err := f.flatten(k)
			if err != nil {
				return nil, err
			}
			fv, err := f.flatten(v)
			if err != nil {
				return nil, err
			}
			c.Content = append(c.Content, fk, fv)
		}
		// Keys given in the mapping itself win over merged ones, and of
		// merged mappings the first one wins.
		for i := 0; i+1 < len(merged); i += 2 {
			if k := merged[i]; !seen[k.Value] {
				seen[k.Value] = true
				c.Content = append(c.Content, k, merged[i+1])
			}
		}
	default:
		for _, item := range n.Content {
			fi, err := f.flatten(item)
			if err != nil {
				return nil, err
			}
			c.Content = append(c.Content, fi)
		}
	}
	return &c, nil
}

// mergeContent returns the flattened keys and values the value of a merge
// key brings in: a mapping or a list of them.
func (f *flattener) mergeContent(v *yaml.Node) ([]*yaml.Node, error) {
	for v.Kind == yaml.AliasNode && v.Alias != nil {
		v = v.Alias
	}
	list := []*yaml.Node{v}
	if v.Kind == yaml.SequenceNode {
		list = v.Content
	}
	var out []*yaml.Node
	for _, m := range list {
		fm, err := f.flatten(m)
		if err != nil {
			return nil, err
		}
		if fm.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("line %d: << must refer to a mapping, got %s", fm.Line, given(fm))
		}
		out = append(out, fm.Content...)
	}
	return out, nil
}

// copyNode returns a deep copy of a flattened node.
func copyNode(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, item := range n.Content {
		c.Content[i] = copyNode(item)
	}
	return &c
}

// blank reports whether n is a value Raku takes as false: no value, an
// empty mapping or list, false, 0, "0" or "". An optional section given
// such a value is left out, as in Raku.
func blank(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		return len(n.Content) == 0
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return true
		case "!!bool":
			return n.Value == "false"
		case "!!int", "!!str":
			return n.Value == "0" || n.Value == ""
		case "!!float":
			f, err := strconv.ParseFloat(n.Value, 64)
			return err == nil && f == 0
		}
	}
	return false
}

// find returns the value of key in flattened mapping n, or nil when n is
// nil, not a mapping, lacks key or has it without a value.
func find(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			if v := n.Content[i+1]; v.ShortTag() != "!!null" {
				return v
			}
			return nil
		}
	}
	return nil
}

// take removes key from flattened mapping n and returns its value, or nil
// when it is missing or has no value.
func take(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			v := n.Content[i+1]
			n.Content = slices.Delete(n.Content, i, i+2)
			if v.ShortTag() == "!!null" {
				return nil
			}
			return v
		}
	}
	return nil
}
