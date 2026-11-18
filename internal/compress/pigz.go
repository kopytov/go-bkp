package compress

import (
	"fmt"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Pigz is compress class pigz: gzip on several cores.
type Pigz struct {
	Cmd       config.Command `yaml:"cmd"`
	AddSuffix config.Suffix  `yaml:"add-suffix"`
	// Level is the compression level, or 0 for the default of the tool.
	Level Level `yaml:"level"`
	// Procs is the number of threads, or 0 for the default of the tool.
	Procs Procs `yaml:"procs"`
}

// NewPigz builds a Pigz from its config section.
func NewPigz(n *yaml.Node) (*Pigz, error) {
	p := Pigz{Cmd: config.Command{"pigz", "-c"}, AddSuffix: "gz"}
	if err := config.Decode(n, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Suffix implements config.Filter.
func (p *Pigz) Suffix(in string) string { return in + "." + string(p.AddSuffix) }

// Stage implements config.Filter. Unlike gzip, pigz exits 0 after a
// warning, so no other status is allowed.
func (p *Pigz) Stage() (pipeline.Stage, error) {
	argv := p.Level.append(slices.Clone(p.Cmd))
	if p.Procs > 0 {
		argv = append(argv, "-p", strconv.Itoa(int(p.Procs)))
	}
	return pipeline.Stage{Name: "compress pigz", Argv: argv}, nil
}

// Procs is a number of threads. 0 leaves the choice to the tool, as Raku
// does.
type Procs int

// UnmarshalYAML implements yaml.Unmarshaler.
func (p *Procs) UnmarshalYAML(n *yaml.Node) error {
	var v int
	if err := n.Decode(&v); err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("line %d: procs %d must not be negative", n.Line, v)
	}
	*p = Procs(v)
	return nil
}
