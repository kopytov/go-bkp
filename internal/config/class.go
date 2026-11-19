package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Source is a src class: the stage that produces the data of an archive.
type Source interface {
	// Suffix is the suffix of the data the source produces, such as "tar".
	Suffix() string
	// Spec returns the start of the pipeline of one attempt: the stage of
	// the source, or a file read as it is in In. What it opens is closed
	// by pipeline.Start or by Spec.Close; on an error it closes it itself.
	Spec() (pipeline.Spec, error)
}

// Filter is a compress, crypt or shape class: a stage between the source
// and the destination.
type Filter interface {
	// Suffix returns the suffix of the stream after the filter, given the
	// suffix of its input.
	Suffix(in string) string
	// Stage returns the stage of the filter for one attempt. What it opens,
	// such as Files, is closed by pipeline.Start or by Spec.Close; on an
	// error it closes it itself.
	Stage() (pipeline.Stage, error)
}

// Dest is a dst class: where the archives of a profile are written and
// rotated. Names passed to it are base names inside the destination; one
// that is not is refused.
type Dest interface {
	// Prefix starts the names of the profile's archives.
	Prefix() string
	// Naming returns how the profile's archives with suffix are named and
	// rotated, by the destination's plan and no-rotate.
	Naming(suffix string) Naming
	// Mount makes the destination ready to use, such as by mounting it.
	Mount(ctx context.Context) error
	// Umount releases what Mount set up, if the config asks for it.
	Umount(ctx context.Context) error
	// List returns the files in the destination, sorted by name.
	List(ctx context.Context) ([]Entry, error)
	// Delete deletes the file name.
	Delete(ctx context.Context, name string) error
	// Open opens the file name for reading. An implementation that reads
	// over the network stops the read once ctx is done.
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	// Write stores the archive name. It calls send with the file to write
	// the archive to, and send returns once the data is written or failed.
	// The archive appears under name only when send succeeds; otherwise
	// nothing is left behind. An error wrapping ErrInPlace means the
	// archive is under name all the same, such as when making that durable
	// failed.
	Write(ctx context.Context, name string, send func(out *os.File) error) error
}

// ErrInPlace marks an error of Dest.Write after the archive was put under
// its name.
var ErrInPlace = errors.New("archive in place")

// Entry is a file in a destination.
type Entry struct {
	Name string
	Size int64
}

// Naming names the archives of a profile and picks those to delete. It
// works on file names only.
type Naming interface {
	// Archives returns the names among names that are archives of the
	// profile, sorted by name.
	Archives(names []string) []string
	// Next returns the name of the archive to write on the date of today.
	Next(names []string, today time.Time) (string, error)
	// Replaced returns the archives that written replaces, to delete once
	// written is in place.
	Replaced(names []string, written string) ([]string, error)
	// Rotate returns the archives to delete by the plan once written is in
	// place; none with no-rotate.
	Rotate(names []string, written string) ([]string, error)
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

// AsSource adapts the constructor of a concrete src class to a Table entry.
func AsSource[C Source](ctor func(*yaml.Node) (C, error)) func(*yaml.Node) (Source, error) {
	return func(n *yaml.Node) (Source, error) {
		c, err := ctor(n)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// AsFilter adapts the constructor of a concrete compress, crypt or shape
// class to a Table entry.
func AsFilter[C Filter](ctor func(*yaml.Node) (C, error)) func(*yaml.Node) (Filter, error) {
	return func(n *yaml.Node) (Filter, error) {
		c, err := ctor(n)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// AsDest adapts the constructor of a concrete dst class to a Table entry.
func AsDest[C Dest](ctor func(*yaml.Node) (C, error)) func(*yaml.Node) (Dest, error) {
	return func(n *yaml.Node) (Dest, error) {
		c, err := ctor(n)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}
