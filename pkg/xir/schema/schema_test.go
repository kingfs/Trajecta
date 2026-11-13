package schema

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// The structure document is the source the Monitor's XIR map renders, so its
// internal consistency is a contract: unique IDs, a reachable tree, valid
// points, and sealed nodes that carry no point (the structural form of "never
// scan encrypted content").

func TestBuildTreeIsConsistent(t *testing.T) {
	s := Build()
	if s.Version != Version {
		t.Fatalf("version = %d, want %d", s.Version, Version)
	}
	if s.Root != "exchange" {
		t.Fatalf("root = %q, want exchange", s.Root)
	}

	byID := make(map[string]Node, len(s.Nodes))
	for _, n := range s.Nodes {
		if n.ID == "" {
			t.Fatal("node with empty ID")
		}
		if _, dup := byID[n.ID]; dup {
			t.Fatalf("duplicate node ID %q", n.ID)
		}
		if n.Name == "" || n.NameZh == "" {
			t.Fatalf("node %q missing bilingual name", n.ID)
		}
		if n.Type == "" {
			t.Fatalf("node %q missing type", n.ID)
		}
		switch n.Kind {
		case KindRoot, KindSection, KindGroup, KindPart, KindField, KindSealed:
		default:
			t.Fatalf("node %q has unknown kind %q", n.ID, n.Kind)
		}
		byID[n.ID] = n
	}

	// Every child reference resolves, and no node is claimed twice.
	parent := make(map[string]string, len(s.Nodes))
	for _, n := range s.Nodes {
		for _, child := range n.Children {
			if _, ok := byID[child]; !ok {
				t.Fatalf("node %q references unknown child %q", n.ID, child)
			}
			if other, ok := parent[child]; ok {
				t.Fatalf("node %q claimed by both %q and %q", child, other, n.ID)
			}
			parent[child] = n.ID
		}
	}

	// Every node is reachable from the root; the root has no parent.
	if _, ok := parent[s.Root]; ok {
		t.Fatal("root has a parent")
	}
	seen := make(map[string]bool, len(s.Nodes))
	var walk func(id string)
	walk = func(id string) {
		if seen[id] {
			t.Fatalf("cycle or duplicate reach at %q", id)
		}
		seen[id] = true
		for _, child := range byID[id].Children {
			walk(child)
		}
	}
	walk(s.Root)
	if len(seen) != len(s.Nodes) {
		for id := range byID {
			if !seen[id] {
				t.Errorf("node %q is not reachable from the root", id)
			}
		}
	}
}

func TestPointsAreValidAndCovered(t *testing.T) {
	s := Build()

	// Every declared point is part of the hook vocabulary.
	covered := make(map[hook.Point]bool)
	for _, n := range s.Nodes {
		if n.Point == "" {
			continue
		}
		if !n.Point.Valid() {
			t.Fatalf("node %q declares unknown point %q", n.ID, n.Point)
		}
		covered[n.Point] = true
	}
	for _, p := range hook.AllPoints() {
		if !covered[p] {
			t.Errorf("hook point %q is not carried by any structure node", p)
		}
	}

	// The point vocabulary is complete, ordered like hook.AllPoints, and
	// bilingual.
	if len(s.Points) != len(hook.AllPoints()) {
		t.Fatalf("point count = %d, want %d", len(s.Points), len(hook.AllPoints()))
	}
	for i, info := range s.Points {
		if info.Point != hook.AllPoints()[i] {
			t.Fatalf("point[%d] = %q, want %q", i, info.Point, hook.AllPoints()[i])
		}
		if info.Name == "" || info.NameZh == "" || info.Section == "" {
			t.Fatalf("point %q missing bilingual name or section", info.Point)
		}
	}
}

func TestSealedNodesCarryNoPoint(t *testing.T) {
	for _, n := range Build().Nodes {
		if n.Kind != KindSealed {
			continue
		}
		if n.Point != "" {
			t.Errorf("sealed node %q carries point %q; sealed content is never scanned", n.ID, n.Point)
		}
		hasSealed, hasNoText := false, false
		for _, f := range n.Flags {
			hasSealed = hasSealed || f == FlagSealed
			hasNoText = hasNoText || f == FlagNoText
		}
		if !hasSealed || !hasNoText {
			t.Errorf("sealed node %q missing sealed/no-text flags: %v", n.ID, n.Flags)
		}
	}
}

func TestBuildReturnsFreshCopies(t *testing.T) {
	a := Build()
	a.Nodes[0].Name = "mutated"
	a.Points[0].Name = "mutated"
	b := Build()
	if b.Nodes[0].Name == "mutated" || b.Points[0].Name == "mutated" {
		t.Fatal("Build shares state between calls")
	}
}

func TestRepresentationsDescribeBothViews(t *testing.T) {
	s := Build()
	if len(s.Representations) != 2 {
		t.Fatalf("representations = %d, want 2 (wire + semantic)", len(s.Representations))
	}
	ids := map[string]bool{}
	for _, r := range s.Representations {
		ids[r.ID] = true
		if r.Name == "" || r.NameZh == "" || r.Description == "" || r.DescriptionZh == "" {
			t.Fatalf("representation %q missing bilingual facts", r.ID)
		}
	}
	if !ids["wire"] || !ids["semantic"] {
		t.Fatalf("representations = %v, want wire and semantic", ids)
	}
}
