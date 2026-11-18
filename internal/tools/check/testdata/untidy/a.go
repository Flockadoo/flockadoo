// Package untidy imports dep directly, but go.mod marks it indirect.
package untidy

import "example.com/dep"

// X is from dep.
var X = dep.Y
