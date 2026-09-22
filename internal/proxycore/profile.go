package proxycore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/metacubex/mihomo/common/yaml"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
)

const meshName = "JUNGO-MESH"

// sanitizeProfile uses an allowlist: subscriptions can describe outbound
// proxies/rules/DNS, never listeners, controller APIs, TUNs or OS routing.
func sanitizeProfile(data []byte, stateDir string) ([]byte, error) {
	if len(data) > 16<<20 {
		return nil, errors.New("profile exceeds 16 MiB")
	}
	input := make(map[string]any)
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &input); err != nil {
			return nil, fmt.Errorf("profile YAML: %w", err)
		}
	}
	out := make(map[string]any)
	for _, key := range []string{"proxies", "proxy-groups", "proxy-providers", "rules", "rule-providers", "sub-rules", "hosts", "dns"} {
		if value, ok := input[key]; ok {
			out[key] = value
		}
	}
	for _, section := range []string{"proxies", "proxy-groups"} {
		if value, exists := out[section]; exists {
			if _, ok := value.([]any); !ok {
				return nil, fmt.Errorf("%s must be a list", section)
			}
		}
		if list, ok := out[section].([]any); ok {
			for _, v := range list {
				if m, ok := v.(map[string]any); ok {
					if name, _ := m["name"].(string); name == meshName || name == selectedName {
						return nil, errors.New("profile uses a reserved JUNGO proxy name")
					}
				}
			}
		}
	}
	for _, section := range []string{"proxy-providers", "rule-providers"} {
		providers, ok := out[section].(map[string]any)
		if !ok {
			continue
		}
		for name, entry := range providers {
			provider, ok := entry.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("provider %s is not an object", name)
			}
			kind, _ := provider["type"].(string)
			switch kind {
			case "http":
				raw, _ := provider["url"].(string)
				u, err := url.Parse(raw)
				if err != nil || u.Scheme != "https" || u.Host == "" {
					return nil, fmt.Errorf("provider %s requires an HTTPS URL", name)
				}
				hash := sha256.Sum256([]byte(section + ":" + name))
				provider["path"] = filepath.Join(stateDir, "providers", hex.EncodeToString(hash[:])+".yaml")
			case "file":
				path, _ := provider["path"].(string)
				clean, err := containedPath(stateDir, path)
				if err != nil {
					return nil, fmt.Errorf("provider %s: %w", name, err)
				}
				provider["path"] = clean
			case "inline":
				delete(provider, "path")
			default:
				return nil, fmt.Errorf("unsupported provider type %q", kind)
			}
		}
	}
	// Reserve the name during mihomo rule parsing, then replace its adapter
	// before applying the configuration; this direct placeholder never runs.
	list, _ := out["proxies"].([]any)
	out["proxies"] = append(list, map[string]any{"name": meshName, "type": "direct"})
	out["mode"] = "rule"
	out["log-level"] = "warning"
	out["ipv6"] = false
	out["find-process-mode"] = "off"
	out["allow-lan"] = false
	out["bind-address"] = "127.0.0.1"
	out["profile"] = map[string]any{"store-selected": false, "store-fake-ip": false}
	dnsConfig, ok := out["dns"].(map[string]any)
	if !ok {
		dnsConfig = make(map[string]any)
	}
	dnsConfig["enable"] = true
	dnsConfig["listen"] = ""
	dnsConfig["ipv6"] = false
	dnsConfig["enhanced-mode"] = "redir-host"
	dnsConfig["use-system-hosts"] = false
	if _, ok = dnsConfig["nameserver"]; !ok {
		dnsConfig["nameserver"] = []string{"223.5.5.5", "1.1.1.1"}
	}
	if _, ok = dnsConfig["fallback-filter"]; !ok {
		dnsConfig["fallback-filter"] = map[string]any{"geoip": false}
	}
	out["dns"] = dnsConfig
	return yaml.Marshal(out)
}

func containedPath(root, path string) (string, error) {
	if path == "" {
		return "", errors.New("provider file path is empty")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(absoluteRoot, path)
	}
	path = filepath.Clean(path)
	if !inside(absoluteRoot, path) {
		return "", errors.New("provider file must stay inside app state directory")
	}
	// Resolve every existing ancestor so a symlink cannot escape the root.
	resolvedRoot := absoluteRoot
	if r, err := filepath.EvalSymlinks(absoluteRoot); err == nil {
		resolvedRoot = r
	}
	ancestor := path
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			if !inside(resolvedRoot, resolved) {
				return "", errors.New("provider symlink escapes app state directory")
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if ancestor == absoluteRoot {
			break
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			break
		}
		ancestor = next
	}
	return path, nil
}
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ValidateProfile parses the sanitized outbound configuration. Validation is
// serialized with Start because mihomo's parser has process-global state.
func ValidateProfile(profile []byte) error {
	dir, err := os.MkdirTemp("", "jungo-profile-validation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	return ValidateProfileInDir(profile, dir)
}

// ValidateProfileInDir keeps provider and geodata parsing inside the caller's
// private state directory, and allows subsequent Start to reuse downloaded data.
func ValidateProfileInDir(profile []byte, stateDir string) error {
	globalMu.Lock()
	defer globalMu.Unlock()
	// Live imports are validated structurally; parsing a second mihomo config
	// can replace global provider/rule references used by the running instance.
	if active != nil {
		stateDir = active.cfg.StateDir
	}
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	data, err := sanitizeProfile(profile, stateDir)
	if err != nil {
		return err
	}
	if active != nil {
		_, err = config.UnmarshalRawConfig(data)
		return err
	}
	if err = os.MkdirAll(filepath.Join(stateDir, "providers"), 0700); err != nil {
		return err
	}
	previousHome := C.Path.HomeDir()
	C.SetHomeDir(stateDir)
	defer C.SetHomeDir(previousHome)
	installHooks()
	previousHook := hookCore.Load()
	validationCore := &Core{cfg: Config{StateDir: stateDir}}
	hookCore.Store(validationCore)
	defer hookCore.Store(previousHook)
	cfg, err := config.Parse(data)
	if cfg != nil {
		closeProviders(cfg)
	}
	return err
}
