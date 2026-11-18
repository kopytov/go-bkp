// Package src holds the src classes: where the data of an archive comes from.
package src

import "github.com/kopytov/go-bkp/internal/config"

// Classes maps the src class names, in lower case, to their constructors.
var Classes = config.Table[config.Source]{}
