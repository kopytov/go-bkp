// Package shape holds the shape classes: rate limits.
package shape

import "github.com/kopytov/go-bkp/internal/config"

// Classes maps the shape class names, in lower case, to their constructors.
var Classes = config.Table[config.Filter]{
	"pv": config.AsFilter(NewPv),
}
