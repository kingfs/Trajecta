package audit

import (
	"github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// NodePoint maps one audit projection node to its XIR hook point. It delegates
// to pkg/xir/audit so the detection-plugin layer (which cannot import this
// package without a cycle) shares the exact same mapping.
func NodePoint(node audit.Node) hook.Point { return audit.NodePoint(node) }
