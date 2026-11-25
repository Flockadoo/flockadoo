package setup

import (
	"fmt"
	"strings"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// HubCacheNotice is the Hugging Face cache the client, hf and the engine use
// (verify.DefaultHFHubCache), and a warning when an old config still has a
// provider.hf_hub_cache line (model revisions plan, "Where models go"). That
// option only ever reached the verifier, so a provider who set it downloaded
// to one folder and was measured in another. It is ignored (H1).
func HubCacheNotice(cfg *config.Config) (resolved, warning string, err error) {
	resolved, err = verify.DefaultHFHubCache()
	if err != nil {
		return "", "", fmt.Errorf("finding the Hugging Face cache: %w", err)
	}
	if set := strings.TrimSpace(cfg.IgnoredHFHubCache); set != "" {
		warning = fmt.Sprintf("provider.hf_hub_cache (%s) is ignored: models are downloaded to, served from and verified in %s. "+
			"Remove the line from the config, and set HF_HUB_CACHE=%s in the environment if you want that folder.",
			set, resolved, set)
	}
	return resolved, warning, nil
}

// NewModelVerifier is the verifier the client measures its served model
// with. It looks in the Hugging Face cache hf and the engine use, or in
// provider.model_path when that is set.
func NewModelVerifier(cfg *config.Config, catalog *verify.Catalog, server *verify.ServerClient) *verify.Verifier {
	return verify.NewVerifier(catalog, server, cfg.PlatformType(), cfg.Provider.ModelPath)
}
