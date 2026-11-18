package src

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Mysql is src class mysql: a dump by mysqldump.
type Mysql struct {
	Cmd config.Command `yaml:"cmd"`
	Ext config.Suffix  `yaml:"suffix"`
	// DBs are the databases to dump; nil dumps them all.
	DBs          config.Strings `yaml:"dbs"`
	DefaultsFile config.File    `yaml:"defaults-file"`
}

// NewMysql builds a Mysql from its config section.
func NewMysql(n *yaml.Node) (*Mysql, error) {
	m := Mysql{
		Cmd: config.Command{"mysqldump", "-ER", "--single-transaction"},
		Ext: "sql",
	}
	if err := config.Decode(n, &m); err != nil {
		return nil, err
	}
	// Strings decodes an empty list to an empty slice, not nil.
	if m.DBs != nil && len(m.DBs) == 0 {
		return nil, fmt.Errorf("line %d: dbs must not be empty: leave it out to dump all databases", n.Line)
	}
	return &m, nil
}

// Suffix implements config.Source.
func (m *Mysql) Suffix() string { return string(m.Ext) }

// Spec implements config.Source.
func (m *Mysql) Spec() (pipeline.Spec, error) {
	argv := slices.Clone(m.Cmd)
	if m.DefaultsFile != "" {
		argv = append(argv, "--defaults-file="+string(m.DefaultsFile))
	}
	if m.DBs == nil {
		argv = append(argv, "--all-databases")
	}
	argv = append(argv, m.DBs...)
	return oneStage("mysql", argv), nil
}
