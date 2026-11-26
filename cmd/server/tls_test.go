package main

import "testing"

func TestIsNonLoopbackListenAddress(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{":8080", true},
		{"0.0.0.0:8080", true},
		{"[::]:8080", true},
		{"127.0.0.1:8080", false},
		{"[::1]:8080", false},
		{"monitor.internal.test:8080", true},
	}
	for _, tc := range cases {
		if got := isNonLoopbackListenAddress(tc.addr); got != tc.want {
			t.Fatalf("isNonLoopbackListenAddress(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
