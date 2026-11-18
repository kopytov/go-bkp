package compress

import (
	"fmt"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Gzip is compress class gzip.
type Gzip struct {
	Cmd       config.Command `yaml:"cmd"`
	AddSuffix config.Suffix  `yaml:"add-suffix"`
	// Level is the compression level, or 0 for the default of the tool.
	Level Level `yaml:"level"`
}

// NewGzip builds a Gzip from its config section.
func NewGzip(n *yaml.Node) (*Gzip, error) {
	g := Gzip{Cmd: config.Command{"gzip", "-c"}, AddSuffix: "gz"}
	if err := config.Decode(n, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// Suffix implements config.Filter.
func (g *Gzip) Suffix(in string) string { return in + "." + string(g.AddSuffix) }

// Stage implements config.Filter. Status 2 of gzip is a warning.
func (g *Gzip) Stage() (pipeline.Stage, error) {
	return pipeline.Stage{Name: "compress gzip", Argv: g.Level.append(slices.Clone(g.Cmd)), Allow: []int{2}}, nil
}

// Level is a compression level from 1 to 9.
type Level int

// UnmarshalYAML implements yaml.Unmarshaler.
func (l *Level) UnmarshalYAML(n *yaml.Node) error {
	var v int
	if err := n.Decode(&v); err != nil {
		return err
	}
	if v < 1 || v > 9 {
		return fmt.Errorf("line %d: level %d is out of range 1-9", n.Line, v)
	}
	*l = Level(v)
	return nil
}

// append adds the option of l to argv, if l is set.
func (l Level) append(argv []string) []string {
	if l == 0 {
		return argv
	}
	return append(argv, "-"+strconv.Itoa(int(l)))
}
