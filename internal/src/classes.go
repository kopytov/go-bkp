// Package src holds the src classes: where the data of an archive comes from.
package src

import (
	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// Classes maps the src class names, in lower case, to their constructors.
var Classes = config.Table[config.Source]{
	"cmd":   config.AsSource(NewCmd),
	"mysql": config.AsSource(NewMysql),
	"redis": config.AsSource(NewRedis),
	"tar":   config.AsSource(NewTar),
}

// oneStage returns the start of a pipeline with the one stage of a source.
func oneStage(class string, argv []string, allow ...int) pipeline.Spec {
	return pipeline.Spec{Stages: []pipeline.Stage{{Name: "src " + class, Argv: argv, Allow: allow}}}
}
