package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/go-gost/core/auth"
	"github.com/go-gost/core/logger"
	xctx "github.com/go-gost/x/ctx"
	"github.com/go-gost/x/internal/plugin"
)

type httpPluginRequest struct {
	Service  string `json:"service"`
	Username string `json:"username"`
	Password string `json:"password"`
	Client   string `json:"client"`
}

type httpPluginResponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
}

type httpPlugin struct {
	url     string
	client  *http.Client
	header  http.Header
	log     logger.Logger
	passMap map[string]string
	passMu  sync.RWMutex
}

// NewHTTPPlugin creates an Authenticator plugin based on HTTP.
func NewHTTPPlugin(name string, url string, opts ...plugin.Option) auth.Authenticator {
	var options plugin.Options
	for _, opt := range opts {
		opt(&options)
	}

	return &httpPlugin{
		url:    url,
		client: plugin.NewHTTPClient(&options),
		header: options.Header,
		log: logger.Default().WithFields(map[string]any{
			"kind":   "auther",
			"auther": name,
		}),
		passMap: make(map[string]string),
	}
}

func (p *httpPlugin) Authenticate(ctx context.Context, user, password string, opts ...auth.Option) (id string, ok bool) {
	if p.client == nil {
		return
	}

	var options auth.Options
	for _, opt := range opts {
		opt(&options)
	}

	var clientAddr string
	if v := xctx.SrcAddrFromContext(ctx); v != nil {
		clientAddr = v.String()
	}

	rb := httpPluginRequest{
		Service:  options.Service,
		Username: user,
		Password: password,
		Client:   clientAddr,
	}

	tmpIp := strings.Split(rb.Client, ":")[0]
	p.passMu.RLock()
	PasswordId, ok := p.passMap[rb.Username+"|"+tmpIp]
	p.passMu.RUnlock()
	p.log.Infof("key: %s,PasswordId: %s", rb.Username+"|"+tmpIp, PasswordId)
	parts := strings.SplitN(PasswordId, ":|:", 2)
	if ok && len(parts) == 2 && parts[0] == password {
		return parts[1], ok
	}
	// ok is false
	ok = false

	v, err := json.Marshal(&rb)
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(v))
	if err != nil {
		return
	}

	if p.header != nil {
		req.Header = p.header.Clone()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	res := httpPluginResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return
	}

	p.passMu.Lock()
	p.passMap[rb.Username+"|"+tmpIp] = rb.Password + ":|:" + res.ID
	p.passMu.Unlock()

	return res.ID, res.OK
}
