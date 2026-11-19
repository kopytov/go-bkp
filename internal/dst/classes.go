// Package dst holds the dst classes: where archives are written, and their
// naming and rotation.
package dst

import "github.com/kopytov/go-bkp/internal/config"

// Classes maps the dst class names, in lower case, to their constructors.
var Classes = config.Table[config.Dest]{
	"local": config.AsDest(NewLocal),
}
