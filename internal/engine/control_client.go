package engine

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/control"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message) }
func (e *Engine) dialer() *net.Dialer {
	return &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: func(network, address string, c syscall.RawConn) error {
		if e.platform == nil {
			return nil
		}
		var denied bool
		if err := c.Control(func(fd uintptr) { denied = !e.platform.Protect(int(fd)) }); err != nil {
			return err
		}
		if denied {
			return errors.New("Android拒绝保护网络套接字")
		}
		return nil
	}}
}
func validateServer(s string) (string, error) {
	u, err := url.Parse(strings.TrimRight(s, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("控制服务地址须为https://主机:端口，不含凭据或路径")
	}
	u.Path = ""
	return u.String(), nil
}
func (e *Engine) controlClient(c Config) (*http.Client, error) {
	cfg, err := secure.PinnedTLS(c.Fingerprint)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DialContext: e.dialer().DialContext, MaxIdleConnsPerHost: 8, ResponseHeaderTimeout: 15 * time.Second}, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("控制服务不允许重定向") }}, nil
}
func apiDo(ctx context.Context, client *http.Client, base, token, method, path string, input, out any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := client.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return &APIError{Status: r.StatusCode, Message: string(b)}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

type pairParams struct {
	Server      string `json:"server"`
	Fingerprint string `json:"fingerprint"`
	ServiceID   string `json:"serviceId"`
	Code        string `json:"code"`
	Name        string `json:"name"`
}

func (e *Engine) pair(p pairParams) error {
	c := e.config()
	if c.Token != "" {
		return errors.New("设备已配对；请先在控制服务撤销后使用新的独立状态目录")
	}
	server, err := validateServer(p.Server)
	if err != nil {
		return err
	}
	c.Server = server
	c.Fingerprint = strings.ToLower(strings.ReplaceAll(p.Fingerprint, ":", ""))
	client, err := e.controlClient(c)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	var info struct {
		ServiceID string `json:"service_id"`
	}
	if err = apiDo(e.ctx, client, server, "", "GET", "/v1/info", nil, &info); err != nil {
		return err
	}
	if p.ServiceID != "" && p.ServiceID != info.ServiceID {
		return errors.New("配对信息与控制服务身份不匹配")
	}
	kb, err := hex.DecodeString(c.PrivateKey)
	if err != nil {
		return err
	}
	key, err := ecdh.X25519().NewPrivateKey(kb)
	if err != nil {
		return err
	}
	var result control.EnrollmentResult
	err = apiDo(e.ctx, client, server, "", "POST", "/v1/enroll", control.Enrollment{ServiceID: info.ServiceID, Code: strings.TrimSpace(p.Code), Name: p.Name, PublicKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), FileTLSFingerprint: e.certFingerprint}, &result)
	if err != nil {
		return err
	}
	c.ServiceID = result.ServiceID
	c.Token = result.Token
	c.Device = result.Device
	return e.commit(c)
}
