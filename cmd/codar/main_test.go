package main

import "testing"

func TestPanelURL(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"0.0.0.0:8801", "http://127.0.0.1:8801/"},
		{"[::]:8801", "http://127.0.0.1:8801/"},
		{":9900", "http://127.0.0.1:9900/"},
		{"127.0.0.1:8801", "http://127.0.0.1:8801/"},
		{"[::1]:8801", "http://[::1]:8801/"},
		{"localhost:8802", "http://localhost:8802/"},
		{"192.168.1.10:8801", "http://192.168.1.10:8801/"},
	} {
		if got := panelURL(tc.addr); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.addr, got, tc.want)
		}
	}
}
