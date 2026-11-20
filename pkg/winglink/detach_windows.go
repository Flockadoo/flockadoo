//go:build windows

package winglink

import "os/exec"

func detach(cmd *exec.Cmd) {}
