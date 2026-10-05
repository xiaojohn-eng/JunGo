package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xiaojohn-eng/JunGo/internal/control"
	"github.com/xiaojohn-eng/JunGo/internal/files"
	"github.com/xiaojohn-eng/JunGo/internal/proxycore"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func decode(p json.RawMessage, out any) error {
	if len(p) == 0 {
		p = []byte("{}")
	}
	return json.Unmarshal(p, out)
}
func (e *Engine) Request(raw string) (string, error) {
	var request Request
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return "", err
	}
	if request.Method == "state" {
		return marshal(e.State())
	}
	if strings.HasPrefix(request.Method, "chat") {
		return e.chatRequest(request)
	}
	if request.Method == "shares" || request.Method == "entries" || request.Method == "mkdir" {
		var p struct {
			DeviceID string `json:"deviceId"`
			ShareID  string `json:"shareId"`
			Path     string `json:"path"`
		}
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		route := "/v1/files/" + request.Method
		method := "GET"
		var input any
		if request.Method == "entries" {
			route += "?share=" + url.QueryEscape(p.ShareID) + "&path=" + url.QueryEscape(p.Path)
		}
		if request.Method == "mkdir" {
			method = "POST"
			input = map[string]string{"shareId": p.ShareID, "path": p.Path}
		}
		ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
		defer cancel()
		b, err := e.fileJSON(ctx, p.DeviceID, method, route, input, nil)
		return string(b), err
	}
	if request.Method == "upload" || request.Method == "download" {
		var t Transfer
		if err := decode(request.Params, &t); err != nil {
			return "", err
		}
		t.Direction = request.Method
		task, err := e.addTransfer(t)
		if err != nil {
			return "", err
		}
		return marshal(task)
	}
	if request.Method == "transferAction" {
		var p struct{ ID, Action string }
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		if err := e.transferAction(p.ID, p.Action); err != nil {
			return "", err
		}
		return marshal(e.State())
	}
	if request.Method == "testProxy" {
		var p struct{ Name string }
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		e.mu.RLock()
		core, closed := e.core, e.closed
		e.mu.RUnlock()
		if closed {
			return "", errors.New("客户端已关闭")
		}
		if core == nil {
			return "", errors.New("请先连接代理")
		}
		ctx, cancel := context.WithTimeout(e.ctx, 12*time.Second)
		defer cancel()
		delay, err := core.TestNode(ctx, p.Name)
		if err != nil {
			return "", err
		}
		return marshal(map[string]int{"delay": delay})
	}
	var importedProfile *importProfileParams
	if request.Method == "importProfile" {
		p := new(importProfileParams)
		if err := decode(request.Params, p); err != nil {
			return "", err
		}
		if p.URL != "" {
			profile, err := e.downloadProfile(p.URL)
			if err != nil {
				return "", err
			}
			p.YAML = profile
		}
		importedProfile = p
	}
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.RLock()
	closed := e.closed
	e.mu.RUnlock()
	if closed {
		return "", errors.New("客户端已关闭")
	}
	c := e.config()
	switch request.Method {
	case "pair":
		var p pairParams
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		if err := e.pair(p); err != nil {
			return "", err
		}
	case "renameDevice":
		var p struct {
			Name string `json:"name"`
		}
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		if p.Name == "" || len(p.Name) > 128 || !utf8.ValidString(p.Name) || strings.TrimSpace(p.Name) != p.Name || strings.IndexFunc(p.Name, func(r rune) bool { return unicode.Is(unicode.C, r) || (unicode.IsSpace(r) && r != ' ') }) >= 0 {
			return "", errors.New("设备名称须为 1–128 字节，不能包含前后空白、控制字符或特殊空格")
		}
		if c.Token == "" || c.Device.ID == "" {
			return "", errors.New("请先配对设备")
		}
		client, err := e.controlClient(c)
		if err != nil {
			return "", err
		}
		defer client.CloseIdleConnections()
		var updated control.Device
		if err := apiDo(e.ctx, client, c.Server, c.Token, "POST", "/v1/device/name", map[string]string{"name": p.Name}, &updated); err != nil {
			return "", err
		}
		if updated.ID != c.Device.ID || updated.Name != p.Name {
			return "", errors.New("控制服务返回的设备身份或名称不匹配")
		}
		c.Device.Name = updated.Name
		if err := e.commit(c); err != nil {
			return "", fmt.Errorf("控制服务已更新名称，但本机保存失败；连接恢复后会重新同步：%w", err)
		}
	case "network":
		old := c
		var p struct {
			Mesh       *bool `json:"mesh"`
			Proxy      *bool `json:"proxy"`
			BypassUIDs []int `json:"bypassUIDs"`
		}
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		if p.Mesh != nil {
			c.MeshEnabled = *p.Mesh
		}
		if p.Proxy != nil {
			c.ProxyEnabled = *p.Proxy
		}
		if p.BypassUIDs != nil {
			for _, uid := range p.BypassUIDs {
				if uid <= 0 || uint64(uid) > 1<<32-1 {
					return "", errors.New("无效的应用UID")
				}
			}
			c.BypassUIDs = p.BypassUIDs
		}
		if c.MeshEnabled && c.Token == "" {
			return "", errors.New("请先配对设备")
		}
		if c.ProxyEnabled && c.Profile == "" {
			return "", errors.New("请先导入Clash配置或订阅")
		}
		e.mu.RLock()
		core := e.core
		e.mu.RUnlock()
		if core != nil {
			if err := core.SetPolicy(c.ProxyEnabled, c.Mode, c.Selected, c.BypassUIDs); err != nil {
				return "", err
			}
		}
		if err := e.commit(c); err != nil {
			if core != nil {
				_ = core.SetPolicy(old.ProxyEnabled, old.Mode, old.Selected, old.BypassUIDs)
			}
			return "", err
		}
		if c.MeshEnabled {
			if err := e.startMesh(); err != nil {
				e.setError(err)
				return "", err
			}
		} else {
			e.stopMesh()
		}
	case "mode", "selectProxy":
		old := c
		var p struct{ Mode, Name string }
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		if request.Method == "mode" {
			if p.Mode != "rule" && p.Mode != "global" && p.Mode != "direct" {
				return "", errors.New("无效的代理模式")
			}
			c.Mode = p.Mode
		} else {
			c.Selected = p.Name
		}
		e.mu.RLock()
		core := e.core
		e.mu.RUnlock()
		oldSelection := old.Selected
		if core != nil {
			oldSelection = core.Selected()
			if err := core.SetPolicy(c.ProxyEnabled, c.Mode, c.Selected, c.BypassUIDs); err != nil {
				return "", err
			}
		} else if request.Method == "selectProxy" && (c.Selected == "" || profileSelection(c.Profile, c.Selected) != c.Selected) {
			return "", errors.New("所选节点不存在，请更新订阅或重新选择")
		}
		if err := e.commit(c); err != nil {
			if core != nil {
				_ = core.SetPolicy(old.ProxyEnabled, old.Mode, oldSelection, old.BypassUIDs)
			}
			return "", err
		}
	case "importProfile":
		p := *importedProfile
		if len(p.YAML) == 0 || len(p.YAML) > 8<<20 {
			return "", errors.New("配置为空或过大")
		}
		if err := proxycore.ValidateProfileInDir([]byte(p.YAML), filepath.Join(e.dir, "proxy")); err != nil {
			return "", err
		}
		c.Profile = p.YAML
		c.ProfileURL = p.URL
		e.mu.RLock()
		core := e.core
		e.mu.RUnlock()
		if core != nil {
			if err := core.LoadProfileWithCommit([]byte(p.YAML), func(selected string) error {
				c.Selected = selected
				return e.commit(c)
			}); err != nil {
				return "", err
			}
		} else {
			c.Selected = profileSelection(c.Profile, c.Selected)
			if err := e.commit(c); err != nil {
				return "", err
			}
		}
	case "shareAdd":
		if e.platform != nil {
			return "", errors.New("安卓首版不提供共享文件服务")
		}
		var s Share
		if err := decode(request.Params, &s); err != nil {
			return "", err
		}
		abs, err := filepath.Abs(s.Path)
		if err != nil {
			return "", err
		}
		s.Path = abs
		if s.Name == "" {
			s.Name = filepath.Base(abs)
		}
		s.ID = secure.Random(12)
		c.Shares = append(append([]Share{}, c.Shares...), s)
		if err = e.validateShares(c.Shares); err != nil {
			return "", err
		}
		if err = e.commit(c); err != nil {
			return "", err
		}
		e.stopMesh()
		if c.MeshEnabled {
			if err = e.startMesh(); err != nil {
				return "", err
			}
		}
	case "shareRemove":
		var p struct{ ID string }
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		shares := []Share{}
		found := false
		for _, s := range c.Shares {
			if s.ID == p.ID {
				found = true
			} else {
				shares = append(shares, s)
			}
		}
		if !found {
			return "", errors.New("共享目录不存在")
		}
		c.Shares = shares
		if err := e.commit(c); err != nil {
			return "", err
		}
		e.stopMesh()
		if c.MeshEnabled {
			if err := e.startMesh(); err != nil {
				return "", err
			}
		}
	case "serviceAdd":
		var mapping PortMapping
		if err := decode(request.Params, &mapping); err != nil {
			return "", err
		}
		if mapping.Network == "" {
			mapping.Network = "tcp"
		}
		if mapping.Port == 0 || mapping.Port == 8443 || mapping.Network != "tcp" || !strings.HasPrefix(mapping.Target, "127.0.0.1:") {
			return "", errors.New("请输入本机127.0.0.1的TCP端口，8443保留用于文件服务")
		}
		for _, p := range c.Services {
			if p.Port == mapping.Port {
				return "", errors.New("该服务端口已配置")
			}
		}
		c.Services = append(c.Services, mapping)
		if err := e.commit(c); err != nil {
			return "", err
		}
		e.stopMesh()
		if c.MeshEnabled {
			if err := e.startMesh(); err != nil {
				return "", err
			}
		}
	case "serviceRemove":
		var p struct{ Port uint16 }
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		services := []PortMapping{}
		for _, s := range c.Services {
			if s.Port != p.Port {
				services = append(services, s)
			}
		}
		c.Services = services
		if err := e.commit(c); err != nil {
			return "", err
		}
		e.stopMesh()
		if c.MeshEnabled {
			if err := e.startMesh(); err != nil {
				return "", err
			}
		}
	case "createPairing", "revokeDevice":
		var p struct {
			AdminToken string `json:"adminToken"`
			DeviceID   string `json:"deviceId"`
		}
		if err := decode(request.Params, &p); err != nil {
			return "", err
		}
		client, err := e.controlClient(c)
		if err != nil {
			return "", err
		}
		defer client.CloseIdleConnections()
		if request.Method == "createPairing" {
			var pair control.PairingCode
			if err = apiDo(e.ctx, client, c.Server, p.AdminToken, "POST", "/v1/pairing-codes", nil, &pair); err != nil {
				return "", err
			}
			payload, _ := json.Marshal(pairParams{Server: c.Server, Fingerprint: c.Fingerprint, ServiceID: pair.ServiceID, Code: pair.Code})
			return marshal(map[string]any{"server": c.Server, "fingerprint": c.Fingerprint, "serviceId": pair.ServiceID, "code": pair.Code, "expiresAt": pair.ExpiresAt, "payload": string(payload)})
		}
		if p.DeviceID == "" || strings.ContainsAny(p.DeviceID, "/\\") {
			return "", errors.New("无效设备ID")
		}
		if err = apiDo(e.ctx, client, c.Server, p.AdminToken, "POST", "/v1/devices/"+p.DeviceID+"/revoke", nil, nil); err != nil {
			return "", err
		}
	default:
		return "", errors.New("未知操作: " + request.Method)
	}
	e.setError(nil)
	return marshal(e.State())
}
func marshal(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
func (e *Engine) validateShares(shares []Share) error {
	dir, err := os.MkdirTemp(e.dir, ".validate-shares-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	list := make([]files.Share, 0, len(shares))
	for _, s := range shares {
		list = append(list, files.Share{ID: s.ID, Name: s.Name, Path: s.Path, ReadOnly: s.ReadOnly})
	}
	service, err := files.New(files.Config{StateDir: dir, Shares: list, Authorize: func(context.Context, *http.Request) (string, error) { return "", errors.New("validation only") }})
	if err != nil {
		return err
	}
	return service.Close()
}
