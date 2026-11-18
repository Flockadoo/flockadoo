// Package x imports the fakes from production code, which section 14 rule 8 forbids.
package x

import "github.com/sentnl/inferoute-node/inferoute-client/internal/testenv"

// N uses the fake.
var N = testenv.Name
