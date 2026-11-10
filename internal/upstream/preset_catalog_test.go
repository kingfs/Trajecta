package upstream

import (
	"testing"

	pkgupstream "github.com/kingfs/Trajecta/pkg/upstream"
)

// The runtime preset registry and the pure catalog must not drift: every
// catalog preset needs a legacy spec so channel validation/defaults can see it.
func TestEveryCatalogPresetHasLegacySpec(t *testing.T) {
	for _, preset := range pkgupstream.Presets() {
		if _, ok := providerPresetRegistry[preset.ID]; !ok {
			t.Errorf("catalog preset %q has no legacy provider spec", preset.ID)
		}
	}
}
