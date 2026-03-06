package admission

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/go-gost/core/admission"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/internal/plugin"
)

type httpPluginRequest struct {
	Service string `json:"service"`
	Addr    string `json:"addr"`
}

type httpPluginResponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id,omitempty"`
}

type httpPlugin struct {
	url    string
	client *http.Client
	header http.Header
	log    logger.Logger

	//新增
	passMap map[string]bool
	passMu  sync.RWMutex
	idMap   map[string]string // 存储 IP 对应的 ID
}

// NewHTTPPlugin creates an Admission plugin based on HTTP.
func NewHTTPPlugin(name string, url string, opts ...plugin.Option) admission.Admission {
	var options plugin.Options
	for _, opt := range opts {
		opt(&options)
	}

	return &httpPlugin{
		url:    url,
		client: plugin.NewHTTPClient(&options),
		header: options.Header,
		log: logger.Default().WithFields(map[string]any{
			"kind":      "admission",
			"admission": name,
		}),
		passMap: make(map[string]bool),
		idMap:   make(map[string]string),
	}
}

func (p *httpPlugin) Admit(ctx context.Context, addr string, opts ...admission.Option) (ok bool) {
	if p.client == nil {
		return false
	}

	var options admission.Options
	for _, opt := range opts {
		opt(&options)
	}

	rb := httpPluginRequest{
		Service: options.Service,
		Addr:    addr,
	}

	tIp := strings.Split(addr, ":")[0]
	p.passMu.RLock()
	pass, ok := p.passMap[tIp]

	p.log.Infof("tIp: %s,pass: %v", tIp, pass)
	p.passMu.RUnlock()
	if ok && pass {
		return pass
	}
	ok = false

	v, err := json.Marshal(&rb)
	if err != nil {
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(v))
	if err != nil {
		return false
	}

	if p.header != nil {
		req.Header = p.header.Clone()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	res := httpPluginResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return false
	}
	p.log.Infof("res: %+v,OK: %v", res, res.OK)
	if !res.OK {
		return false
	}
	p.passMu.Lock()
	p.passMap[tIp] = res.OK
	// 存储返回的 ID（优先使用小写 id，如果没有则使用大写 ID）
	admissionID := res.ID
	if admissionID != "" {
		p.idMap[tIp] = admissionID
		p.log.Debugf("admission: stored ID for %s, id=%s", tIp, admissionID)
	} else {
		p.log.Debugf("admission: no ID returned for %s", tIp)
	}
	p.passMu.Unlock()

	return res.OK
}

// GetID 获取指定地址对应的 ID
func (p *httpPlugin) GetID(addr string) string {
	tIp := strings.Split(addr, ":")[0]
	p.passMu.RLock()
	defer p.passMu.RUnlock()
	id := p.idMap[tIp]
	p.log.Debugf("admission GetID: addr=%s, ip=%s, id=%s", addr, tIp, id)
	return id
}
