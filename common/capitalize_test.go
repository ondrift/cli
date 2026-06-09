package common

import "testing"

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "Hello"},
		{"Hello", "Hello"},
		{"a", "A"},
		{"", ""},
		{"123abc", "123abc"},
		{"über", "Über"},
		{"日本語", "日本語"}, // CJK chars don't have upper/lower
	}
	for _, tt := range tests {
		got := CapitalizeFirst(tt.input)
		if got != tt.want {
			t.Errorf("CapitalizeFirst(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
