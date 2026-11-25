package setup

import (
	"fmt"
	"strings"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// HubCacheNotice is the Hugging Face cache the client, hf and the engine use
// (verify.DefaultHFHubCache), and a warning when the config still sets
// provider.hf_hub_cache (model revisions plan, "Where models go"). That
// option only ever reached the verifier, so a provider who set it downloaded
// to one folder and was measured in another. This release still passes it to
// the verifier as before; the next release removes it.
func HubCacheNotice(cfg *config.Config) (resolved, warning string, err error) {
	resolved, err = verify.DefaultHFHubCache()
	if err != nil {
		return "", "", fmt.Errorf("finding the Hugging Face cache: %w", err)
	}
	if set := strings.TrimSpace(cfg.Provider.HFHubCache); set != "" {
		warning = fmt.Sprintf("provider.hf_hub_cache is set to %s, but models are downloaded to and served from %s. "+
			"The option is ignored from the next release: remove it from the config, and set HF_HUB_CACHE=%s in the environment instead if you want that folder.",
			set, resolved, set)
	}
	return resolved, warning, nil
}

// NewModelVerifier is the verifier the client measures its served model
// with. Stub until H1: it still passes provider.hf_hub_cache on.
func NewModelVerifier(cfg *config.Config, catalog *verify.Catalog, server *verify.ServerClient) *verify.Verifier {
	return verify.NewVerifier(catalog, server, cfg.PlatformType(), cfg.Provider.HFHubCache, cfg.Provider.ModelPath)
}
