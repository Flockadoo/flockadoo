package setup

import (
	"path/filepath"
	"testing"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
)

// Plan item Q2, finding QUAL-3: the defaults are written once. A config the
// wizard starts from scratch has exactly the defaults Load gives a missing
// file, so the two cannot drift apart.
func TestAFreshConfigHasTheLoadDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.yaml")
	fresh, err := loadOrFresh(missing)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(missing)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Server != loaded.Server {
		t.Errorf("server: wizard %+v, Load %+v", fresh.Server, loaded.Server)
	}
	if fresh.Provider.URL != loaded.Provider.URL || fresh.Provider.LLMURL != loaded.Provider.LLMURL ||
		fresh.Provider.Engine != loaded.Provider.Engine || fresh.Provider.LLMTimeoutSeconds != loaded.Provider.LLMTimeoutSeconds {
		t.Errorf("provider: wizard %+v, Load %+v", fresh.Provider, loaded.Provider)
	}
	if fresh.Logging != loaded.Logging {
		t.Errorf("logging: wizard %+v, Load %+v", fresh.Logging, loaded.Logging)
	}
}
