// Package crypt holds the crypt classes.
package crypt

import "github.com/kopytov/go-bkp/internal/config"

// Classes maps the crypt class names, in lower case, to their constructors.
var Classes = config.Table[config.Filter]{}
