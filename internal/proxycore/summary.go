package proxycore

import (
	"github.com/metacubex/mihomo/common/yaml"
	"sort"
)

// ProfileNodes exposes only display metadata while the VPN is stopped. It must
// never return subscription credentials, server endpoints or raw YAML.
func ProfileNodes(data []byte, selected string) []NodeInfo {
	var profile struct {
		Proxies []struct {
			Name string `yaml:"name"`
			Type string `yaml:"type"`
		} `yaml:"proxies"`
		Groups []struct {
			Name    string   `yaml:"name"`
			Type    string   `yaml:"type"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if yaml.Unmarshal(data, &profile) != nil {
		return nil
	}
	nodes := []NodeInfo{}
	for _, p := range profile.Proxies {
		if p.Name != "" && !isReserved(p.Name) {
			nodes = append(nodes, NodeInfo{Name: p.Name, Type: p.Type, Selected: p.Name == selected, Delay: -1})
		}
	}
	for _, p := range profile.Groups {
		if p.Name != "" && !isReserved(p.Name) {
			nodes = append(nodes, NodeInfo{Name: p.Name, Type: p.Type, Selected: p.Name == selected, Delay: -1, Members: p.Proxies})
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes
}
