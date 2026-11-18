package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Source is a src class: the stage that produces the data of an archive.
type Source interface {
	// Suffix is the suffix of the data the source produces, such as "tar".
	Suffix() string
}

// Filter is a compress, crypt or shape class: a stage between the source
// and the destination.
type Filter interface {
	// Suffix returns the suffix of the stream after the filter, given the
	// suffix of its input.
	Suffix(in string) string
}

// Dest is a dst class: where the archives of a profile are written and
// rotated.
type Dest interface {
	// Prefix starts the names of the profile's archives.
	Prefix() string
}

// Table maps the class names of one namespace, in lower case, to their
// constructors. A constructor gets the section of the config without its
// class key, decodes it with Decode and checks its values; it starts no
// process and does not touch the network.
type Table[T any] map[string]func(*yaml.Node) (T, error)

// Classes holds a class table for each namespace of a profile.
type Classes struct {
	Src      Table[Source]
	Compress Table[Filter]
	Crypt    Table[Filter]
	Shape    Table[Filter]
	Dst      Table[Dest]
}

// build takes the class key out of section n and constructs that class from
// what is left. n must be a copy owned by the caller.
func build[T any](table Table[T], n *yaml.Node) (T, error) {
	var zero T
	if n.Kind != yaml.MappingNode {
		return zero, fmt.Errorf("line %d: want a mapping with a class, got %s", n.Line, given(n))
	}
	cn := take(n, "class")
	if cn == nil {
		return zero, fmt.Errorf("line %d: class is required", n.Line)
	}
	name, err := scalar(cn)
	if err != nil {
		return zero, fmt.Errorf("class: %w", err)
	}
	if name == "" {
		return zero, fmt.Errorf("line %d: class is empty", cn.Line)
	}
	name = strings.ToLower(name)
	ctor, ok := table[name]
	if !ok {
		return zero, fmt.Errorf("line %d: unknown class %q%s", cn.Line, name, known(table))
	}
	v, err := ctor(n)
	if err != nil {
		return zero, fmt.Errorf("class %s: %w", name, err)
	}
	return v, nil
}

func known[T any](table Table[T]) string {
	if len(table) == 0 {
		return ""
	}
	return ", known: " + strings.Join(slices.Sorted(maps.Keys(table)), ", ")
}
