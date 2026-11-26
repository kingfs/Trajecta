package store

import "testing"

func TestOverviewCountByRejectsUnknownColumn(t *testing.T) {
	s := &Store{}
	if _, err := s.overviewCountBy("name; DROP TABLE logs", "", nil, 1); err == nil {
		t.Fatal("overviewCountBy(unknown column) = nil, want error")
	}
}

func TestRequestCleanupDeleteByColumnRejectsUnknownTable(t *testing.T) {
	s := &Store{}
	if _, err := requestCleanupDeleteByColumn(nil, s, "logs; DROP TABLE users", "trace_id", []string{"x"}); err == nil {
		t.Fatal("requestCleanupDeleteByColumn(unknown table) = nil, want error")
	}
	if _, err := requestCleanupDeleteByColumn(nil, s, "logs", "name; DROP TABLE users", []string{"x"}); err == nil {
		t.Fatal("requestCleanupDeleteByColumn(unknown column) = nil, want error")
	}
}
