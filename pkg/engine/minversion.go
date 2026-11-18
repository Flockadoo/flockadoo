package engine

import "errors"

// Stub with today's behaviour: no release is too old.

const MinVLLMVersion = "0.30.0"

var ErrEngineTooOld = errors.New("engine release is too old")

func TooOld(kind Kind, version string) bool { return false }

func CheckVersion(kind Kind, version string) error { return nil }
