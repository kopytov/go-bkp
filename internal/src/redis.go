package src

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Redis is src class redis: the dump file Redis writes, read as it is.
type Redis struct {
	Ext  config.Suffix `yaml:"suffix"`
	Dump config.File   `yaml:"dump"`
}

// NewRedis builds a Redis from its config section.
func NewRedis(n *yaml.Node) (*Redis, error) {
	r := Redis{Ext: "rdb"}
	if err := config.Decode(n, &r); err != nil {
		return nil, err
	}
	if r.Dump == "" {
		return nil, fmt.Errorf("line %d: dump is required", n.Line)
	}
	return &r, nil
}

// Suffix implements config.Source.
func (r *Redis) Suffix() string { return string(r.Ext) }

// Spec implements config.Source: the dump opened as the input of the
// pipeline, with no process.
func (r *Redis) Spec() (pipeline.Spec, error) {
	f, err := os.Open(string(r.Dump))
	if err != nil {
		return pipeline.Spec{}, fmt.Errorf("open dump: %w", err)
	}
	return pipeline.Spec{In: f}, nil
}
