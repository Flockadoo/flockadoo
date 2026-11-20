package version

import (
	"fmt"
	"runtime"
)

// Build identity. Release builds set these with -ldflags -X.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// UserAgent identifies this binary to the Flockadoo node.
func UserAgent() string {
	return fmt.Sprintf("flockadoo/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH) //nolint:forbidigo // the User-Agent names the platform this binary was built for
}
