package main

import "testing"

func TestWebTokenSettings(t *testing.T) {
	for _, test := range []struct {
		name, conf, env string
		enabled         bool
		ttl             int64
	}{
		{"default", "", "", false, 3600},
		{"enabled-by-config", "use_go_fileserver=true\n", "false", true, 3600},
		{"enabled-by-one", "use_go_fileserver=1\n", "", true, 3600},
		{"enabled-by-env", "use_go_fileserver=false\n", "true", true, 3600},
		{"invalid-switch", "use_go_fileserver=TRUE\n", "TRUE", false, 3600},
		{"quoted-value", "use_go_fileserver=\"true\"\n", "", false, 3600},
		{"inline-text", "use_go_fileserver=true # comment\n", "", false, 3600},
		{"decimal-leading-zero", "web_token_expire_time=018\n", "", false, 18},
		{"zero", "web_token_expire_time=0\n", "", false, 3600},
		{"negative", "web_token_expire_time=-1\n", "", false, 3600},
		{"malformed-ttl", "web_token_expire_time=1 # comment\n", "", false, 3600},
		{"max-ttl", "web_token_expire_time=2147483647\n", "", false, 2147483647},
		{"overflow", "web_token_expire_time=2147483648\n", "", false, 3600},
	} {
		t.Run(test.name, func(t *testing.T) {
			enabled, ttl, err := webTokenSettings([]byte("[fileserver]\n"+test.conf), test.env)
			if err != nil || enabled != test.enabled || ttl != test.ttl {
				t.Fatalf("mode = %v, TTL = %d, error = %v", enabled, ttl, err)
			}
		})
	}
}
