package detector

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir/hook"
	"github.com/kingfs/Trajecta/pkg/xir/schema"
)

// A scanner that declares a point with no producer would be a silent no-op in
// the audit lane: the map would light the node up while no detection could ever
// run there. Pin every declared point to the live vocabulary; scanner points
// must also be produced by the audit projection. Observation-plugin points are
// catalog metadata (the analyzer scopes itself over the IR), so a reserved
// point there documents a field the detector reads rather than an audit hook.
func TestPluginPointsAreWired(t *testing.T) {
	known := map[hook.Point]bool{}
	reserved := map[hook.Point]bool{}
	for _, info := range schema.Build().Points {
		known[info.Point] = true
		if info.Reserved {
			reserved[info.Point] = true
		}
	}
	for _, plugin := range Plugins() {
		for _, point := range plugin.Points {
			if !known[point] {
				t.Errorf("plugin %q declares unknown point %q", plugin.ID, point)
				continue
			}
			if plugin.Contract == ContractScanner && reserved[point] {
				t.Errorf("scanner %q declares reserved point %q (no audit projection node produces it)", plugin.ID, point)
			}
		}
	}
}
