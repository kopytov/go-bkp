package src

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Cmd is src class cmd: the output of any command.
type Cmd struct {
	Cmd config.Command `yaml:"cmd"`
	// Ext is the suffix of the data, such as "tar.zst".
	Ext config.Suffix `yaml:"suffix"`
}

// NewCmd builds a Cmd from its config section.
func NewCmd(n *yaml.Node) (*Cmd, error) {
	var c Cmd
	if err := config.Decode(n, &c); err != nil {
		return nil, err
	}
	if c.Cmd == nil {
		return nil, fmt.Errorf("line %d: cmd is required", n.Line)
	}
	if c.Ext == "" {
		return nil, fmt.Errorf("line %d: suffix is required", n.Line)
	}
	return &c, nil
}

// Suffix implements config.Source.
func (c *Cmd) Suffix() string { return string(c.Ext) }

// Spec implements config.Source.
func (c *Cmd) Spec() (pipeline.Spec, error) {
	return oneStage("cmd", slices.Clone(c.Cmd)), nil
}
