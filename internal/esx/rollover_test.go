package esx

import "testing"

func TestRolloverPrefix(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"logs-app-2026.10.21", "logs-app-", true},
		{"logs-app-2026-10", "logs-app-", true},
		{"metrics_20261021", "metrics_", true},
		{"orders-000123", "orders-", true},
		{"logs-2026.10", "logs-", true},
		{".ds-logs-2026.10.21-000001", ".ds-logs-", true},
		{"orders", "", false},
		{"a-1", "", false},
		{"v-12345", "", false},
		{"2026.10.21", "", false},
	}
	for _, tc := range cases {
		got, ok := RolloverPrefix(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("RolloverPrefix(%q) = %q %v", tc.in, got, ok)
		}
	}
}
