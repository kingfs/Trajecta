package audit

import (
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// NodePoint maps one audit projection node to the XIR hook point it belongs to.
// The detection plugins declare the points they act on, and the audit lane runs
// a plugin only on the nodes whose point it declares.
//
// A sealed block (encrypted reasoning, media bytes, protocol-native opaque
// state) maps to the empty point: it carries no Text by construction, so no
// text detector is ever fed it. This is the structural form of "skip the
// encrypted content" — the field projection excludes it rather than a detector
// having to recognise and drop it.
func NodePoint(node audit.Node) hook.Point {
	switch node.Kind {
	case string(xir.PartSealed):
		return ""
	case string(xir.PartToolCall):
		if node.Section == audit.SectionRequest {
			return hook.PointRequestToolCall
		}
		return hook.PointResponseToolCall
	case string(xir.PartToolResult):
		if node.Section == audit.SectionRequest {
			return hook.PointRequestToolResult
		}
		return hook.PointResponseToolResult
	case string(xir.PartReasoning):
		if node.Section == audit.SectionResponse {
			return hook.PointResponseReasoning
		}
		return hook.PointRequestMessage
	case "tool":
		// A tool declaration container lives on the request side.
		return hook.PointRequestToolDecl
	case "message":
		if node.Section == audit.SectionRequest {
			if node.Role == xir.RoleSystem || node.Role == xir.RoleDeveloper {
				return hook.PointRequestSystem
			}
			return hook.PointRequestMessage
		}
		return hook.PointResponseOutput
	default:
		// Text, refusal and any other text-bearing part. Media parts carry no
		// Text and are skipped by the caller before the point matters.
		if node.Section == audit.SectionRequest {
			if node.Role == xir.RoleSystem || node.Role == xir.RoleDeveloper {
				return hook.PointRequestSystem
			}
			return hook.PointRequestMessage
		}
		return hook.PointResponseOutput
	}
}
