package main

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"flag", "env", "flag"},
		{"", "env", "env"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := firstNonEmpty(c.a, c.b); got != c.want {
			t.Errorf("firstNonEmpty(%q, %q) = %q, esperava %q", c.a, c.b, got, c.want)
		}
	}
}
