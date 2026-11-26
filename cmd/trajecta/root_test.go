package main

import "testing"

func TestNewRootCommandBindsPersistentFlags(t *testing.T) {
	root, err := newRootCommand()
	if err != nil {
		t.Fatalf("newRootCommand() error = %v", err)
	}
	if root == nil {
		t.Fatal("newRootCommand() = nil")
	}
	if root.PersistentFlags().Lookup("config") == nil || root.PersistentFlags().Lookup("format") == nil {
		t.Fatal("persistent --config/--format flags are not registered")
	}
}
