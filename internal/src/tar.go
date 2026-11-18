package src

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Tar is src class tar: files packed by tar.
type Tar struct {
	Cmd     config.Command `yaml:"cmd"`
	Ext     config.Suffix  `yaml:"suffix"`
	Files   config.Strings `yaml:"files"`
	Exclude config.Strings `yaml:"exclude"`
	// Cwd is the directory files are relative to, or empty.
	Cwd config.Dir `yaml:"cwd"`
}

// NewTar builds a Tar from its config section.
func NewTar(n *yaml.Node) (*Tar, error) {
	t := Tar{
		Cmd: config.Command{"tar", "--warning=none", "--numeric-owner", "-cf", "-"},
		Ext: "tar",
	}
	if err := config.Decode(n, &t); err != nil {
		return nil, err
	}
	if len(t.Files) == 0 {
		return nil, fmt.Errorf("line %d: files is required and must not be empty", n.Line)
	}
	return &t, nil
}

// Suffix implements config.Source.
func (t *Tar) Suffix() string { return string(t.Ext) }

// Spec implements config.Source. Status 1 of GNU tar, a file changed while
// it was read, is allowed: it is normal on live data. A cmd that is not GNU
// tar gets the same allowance.
func (t *Tar) Spec() (pipeline.Spec, error) {
	argv := slices.Clone(t.Cmd)
	if t.Cwd != "" {
		argv = append(argv, "-C", string(t.Cwd))
	}
	for _, x := range t.Exclude {
		argv = append(argv, "--exclude="+x)
	}
	argv = append(argv, t.Files...)
	return oneStage("tar", argv, 1), nil
}
