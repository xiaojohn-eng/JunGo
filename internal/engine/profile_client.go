package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/proxycore"
)

type importProfileParams struct{ URL, YAML string }

var errProfileRedirect = errors.New("订阅重定向不安全或次数过多")

func (e *Engine) downloadProfile(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("订阅地址必须是有效的HTTPS地址，不含登录凭据或片段")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: e.dialer().DialContext}, Timeout: 45 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || r.URL.User != nil || len(via) > 5 {
			return errProfileRedirect
		}
		return nil
	}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(e.ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", errors.New("订阅请求创建失败")
	}
	req.Header.Set("User-Agent", "clash.meta/JunGo")
	req.Header.Set("Accept", "application/yaml, text/yaml, text/plain, */*")
	response, err := client.Do(req)
	if err != nil {
		return "", profileDownloadError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("订阅服务返回失败，请检查订阅是否有效")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return "", profileDownloadError(err)
	}
	if len(data) > 8<<20 {
		return "", errors.New("订阅配置超过8MiB")
	}
	return string(data), nil
}

// net/http errors embed the full request URL, commonly including an airport
// subscription token. Return useful error categories without that private URL.
func profileDownloadError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return errors.New("订阅更新已取消")
	case errors.Is(err, errProfileRedirect):
		return errProfileRedirect
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return errors.New("订阅下载超时，请检查网络后重试")
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return errors.New("订阅HTTPS证书验证失败")
	}
	return errors.New("订阅连接或读取失败，请检查网络和订阅地址")
}

func profileSelection(profile, selected string) string {
	if selected == "DIRECT" || selected == "REJECT" || selected == "REJECT-DROP" {
		return selected
	}
	nodes := proxycore.ProfileNodes([]byte(profile), selected)
	for _, node := range nodes {
		if node.Selected {
			return selected
		}
	}
	if len(nodes) > 0 {
		return nodes[0].Name
	}
	return "DIRECT"
}
