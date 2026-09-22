package proxycore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOfflineProfileNodesExposeOnlyMetadata(t *testing.T) {
	input := []byte("proxies:\n  - name: node\n    type: socks5\n    server: private.example\n    port: 1080\n    password: do-not-expose\nproxy-groups:\n  - name: group\n    type: select\n    proxies: [node]\n")
	nodes := ProfileNodes(input, "node")
	if len(nodes) != 2 {
		t.Fatalf("expected group and node, got %d", len(nodes))
	}
	data, _ := json.Marshal(nodes)
	if strings.Contains(string(data), "private.example") || strings.Contains(string(data), "do-not-expose") {
		t.Fatal("credentials exposed")
	}
	for _, n := range nodes {
		if n.Delay != -1 {
			t.Fatal("offline latency must be unknown")
		}
		if n.Name == "node" && !n.Selected {
			t.Fatal("selection lost")
		}
		if n.Name == "group" && (len(n.Members) != 1 || n.Members[0] != "node") {
			t.Fatal("group lost")
		}
	}
}
