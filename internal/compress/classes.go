// Package compress holds the compress classes.
package compress

import "github.com/kopytov/go-bkp/internal/config"

// Classes maps the compress class names, in lower case, to their constructors.
var Classes = config.Table[config.Filter]{}
