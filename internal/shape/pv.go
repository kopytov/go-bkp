package shape

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Pv is shape class pv: the stream passed on no faster than RateLimit.
type Pv struct {
	Cmd       config.Command `yaml:"cmd"`
	RateLimit Rate           `yaml:"rate-limit"`
}

// NewPv builds a Pv from its config section.
func NewPv(n *yaml.Node) (*Pv, error) {
	p := Pv{Cmd: config.Command{"pv", "-q"}, RateLimit: "1m"}
	if err := config.Decode(n, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Suffix implements config.Filter: pv does not change the data.
func (p *Pv) Suffix(in string) string { return in }

// Stage implements config.Filter.
func (p *Pv) Stage() (pipeline.Stage, error) {
	argv := append(slices.Clone(p.Cmd), "-L", string(p.RateLimit))
	return pipeline.Stage{Name: "shape pv", Argv: argv}, nil
}

// Rate is bytes a second, with an optional suffix k, m, g or t, as pv -L
// takes it. Only an empty one is refused here; pv checks the rest.
type Rate string

// UnmarshalYAML implements yaml.Unmarshaler.
func (r *Rate) UnmarshalYAML(n *yaml.Node) error {
	var v string
	if err := n.Decode(&v); err != nil {
		return err
	}
	if v == "" {
		return fmt.Errorf("line %d: rate-limit must not be empty", n.Line)
	}
	*r = Rate(v)
	return nil
}
