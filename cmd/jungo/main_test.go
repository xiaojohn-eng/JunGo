package main

import "testing"

func TestLocalRPCURLRejectsCredentialExfiltrationTargets(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:1234@attacker.example", "http://127.0.0.1:1234.evil.example",
		"http://attacker.example:1234", "https://127.0.0.1:1234", "http://127.0.0.1:0",
		"http://127.0.0.1:65536", "http://127.0.0.1", "http://127.0.0.1:1234/?token=x",
		"http://127.0.0.1:1234/path", "http://127.0.0.1:1234/#fragment",
	} {
		if _, err := localRPCURL(raw); err == nil {
			t.Fatalf("unsafe endpoint accepted: %q", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:1234", "http://127.0.0.1:1234/"} {
		endpoint, err := localRPCURL(raw)
		if err != nil || endpoint != "http://127.0.0.1:1234/v1/rpc" {
			t.Fatalf("valid endpoint: %s %v", endpoint, err)
		}
	}
}
