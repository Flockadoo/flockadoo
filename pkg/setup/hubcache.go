package setup

import "github.com/Flockadoo/flockadoo/internal/config"

// HubCacheNotice is the Hugging Face cache the client uses, and a warning.
func HubCacheNotice(cfg *config.Config) (resolved, warning string, err error) {
	return "", "", nil
}
