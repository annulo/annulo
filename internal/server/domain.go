package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// 服务端部署的域名和证书（docs/server-deploy.md「域名和证书」）：不放前置代理，证书由 Annulo 自己签、存、续。
// 装好后先用 http://<IP> 打开；管理员在 设置 → 域名 里填域名，Annulo 先自检（这个域名的请求能不能打回本机），
// 通过就当场向 Let's Encrypt 申请证书（HTTP-01 / TLS-ALPN-01，certmagic），之后自动续期；状态、到期时间、失败原因在面板上看。
//
// 端口：HTTP 是 ANNULO_PORT（镜像里 8080，映射成 80），HTTPS 是 ANNULO_TLS_PORT（镜像里 8443，映射成 443）。
// 绑了域名、证书好了以后，打到这个域名的 http 请求跳到 https；用 IP 打开的照旧是 http（找回、排障用）。

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

type domainManager struct {
	dir  string
	cfg  *certmagic.Config
	acme *certmagic.ACMEIssuer

	mu      sync.Mutex
	data    domainData
	issuing bool
	checks  map[string]time.Time // 自检用的一次性 token
}

type domainData struct {
	Domain    string    `json:"domain,omitempty"`
	Email     string    `json:"email,omitempty"`
	BoundAt   time.Time `json:"bound_at,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	ErrorAt   time.Time `json:"error_at,omitempty"`
}

func newDomainManager(dir string) *domainManager {
	d := &domainManager{dir: dir, checks: map[string]time.Time{}}
	if b, err := os.ReadFile(filepath.Join(dir, "domain.json")); err == nil {
		json.Unmarshal(b, &d.data)
	}
	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return cfg, nil }, Logger: zap.NewNop()})
	cfg = certmagic.New(cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: filepath.Join(dir, "certs")},
		Logger:  zap.NewNop(),
		OnEvent: d.onEvent,
	})
	email := brand.Env("ACME_EMAIL")
	if email == "" {
		email = d.data.Email
	}
	issuer := certmagic.ACMEIssuer{CA: certmagic.LetsEncryptProductionCA, Email: email, Agreed: true, Logger: zap.NewNop()}
	if ca := brand.Env("ACME_CA"); ca != "" { // 测试：Pebble 这类 ACME 服务
		issuer.CA, issuer.TestCA = ca, ""
	}
	if root := brand.Env("ACME_CA_ROOT"); root != "" {
		if pem, err := os.ReadFile(root); err == nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			issuer.TrustedRoots = pool
		}
	}
	d.acme = certmagic.NewACMEIssuer(cfg, issuer)
	cfg.Issuers = []certmagic.Issuer{d.acme}
	d.cfg = cfg
	if d.data.Domain != "" {
		go func() {
			if err := cfg.ManageAsync(context.Background(), []string{d.data.Domain}); err != nil {
				d.fail(err)
			}
		}()
	}
	return d
}

func (d *domainManager) save() {
	b, _ := json.MarshalIndent(d.data, "", "  ")
	os.WriteFile(filepath.Join(d.dir, "domain.json"), b, 0o600)
}

func (d *domainManager) fail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.data.LastError, d.data.ErrorAt = err.Error(), time.Now()
	d.save()
}

// onEvent：证书签到、续期失败记下来，面板显示
func (d *domainManager) onEvent(_ context.Context, event string, data map[string]any) error {
	switch event {
	case "cert_obtained":
		d.mu.Lock()
		d.data.LastError, d.data.ErrorAt = "", time.Time{}
		d.save()
		d.mu.Unlock()
	case "cert_failed":
		if e, ok := data["error"].(error); ok {
			d.fail(e)
		}
	}
	return nil
}

// domain 是绑定的域名（没绑是空的）。
func (d *domainManager) domain() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.data.Domain
}

// cert 是这个域名现在用的证书（还没有返回 nil）。
func (d *domainManager) cert(name string) *x509.Certificate {
	if name == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := d.cfg.CacheManagedCertificate(ctx, name)
	if err != nil || c.Leaf == nil {
		return nil
	}
	return c.Leaf
}

// TLSConfig 给 HTTPS 监听用：按 SNI 取证书，也接 TLS-ALPN-01 的验证。
func (d *domainManager) TLSConfig() *tls.Config {
	c := d.cfg.TLSConfig()
	c.NextProtos = append([]string{"h2", "http/1.1"}, c.NextProtos...)
	return c
}

// HTTPHandler 包在 HTTP 监听外面：ACME 的 HTTP-01 验证、自检、绑了域名有了证书以后跳到 https。
func (d *domainManager) HTTPHandler(next http.Handler) http.Handler {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := strings.CutPrefix(brand.CanonicalPath(r.URL.Path), "/_shuttle/domain-check/"); ok {
			d.mu.Lock()
			_, ok := d.checks[tok]
			d.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			io.WriteString(w, tok)
			return
		}
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if dom := d.domain(); r.TLS == nil && host == dom && d.cert(dom) != nil {
			http.Redirect(w, r, "https://"+dom+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
	return d.acme.HTTPChallengeHandler(h)
}

// selfCheck：从这台服务器自己访问 http://<域名>/_annulo/domain-check/<token>，看请求能不能打回本机
// （DNS 解析对了、80 端口映射对了、防火墙开了）。不通的话签证书一定失败，先说清楚哪里不对。
func (d *domainManager) selfCheck(ctx context.Context, name string) error {
	b := make([]byte, 12)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	d.mu.Lock()
	d.checks[tok] = time.Now()
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.checks, tok)
		d.mu.Unlock()
	}()
	ips, err := net.DefaultResolver.LookupHost(ctx, name)
	if err != nil || len(ips) == 0 {
		return i18n.Errorf("%s 还没有解析：在域名服务商那里加一条 A 记录指向这台服务器的 IP，生效后再试", "%s doesn't resolve yet: add an A record pointing to this server's IP at your DNS provider, then try again once it takes effect", name)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", "http://"+name+"/_annulo/domain-check/"+tok, nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return i18n.Errorf("%s 解析到 %s，但从这台服务器访问它的 80 端口不通（%v）：检查解析的 IP 是不是这台服务器、80 端口有没有映射到 Annulo、防火墙和安全组有没有放行",
			"%s resolves to %s, but its port 80 can't be reached from this server (%v): check that the IP is this server's, port 80 is mapped to Annulo, and the firewall / security group allows it", name, strings.Join(ips, ", "), err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	resp.Body.Close()
	if string(body) != tok {
		return i18n.Errorf("%s 解析到 %s，但请求没有打到这个 Annulo（HTTP %d）：解析的 IP 是不是别的服务器，或者前面还有别的程序占着 80 端口",
			"%s resolves to %s, but the request didn't reach this Annulo (HTTP %d): the IP may belong to another server, or another program holds port 80 in front", name, strings.Join(ips, ", "), resp.StatusCode)
	}
	return nil
}

// bind：绑域名并当场签证书。force 跳过自检（比如服务器访问不到自己的公网地址，但外面能访问）。
func (d *domainManager) bind(ctx context.Context, name, email string, force bool) error {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if !domainRe.MatchString(name) {
		return i18n.New("域名不对：写成 annulo.example.com 这样，不带 http:// 和路径", "Invalid domain: write it like annulo.example.com, without http:// or a path")
	}
	d.mu.Lock()
	if d.issuing {
		d.mu.Unlock()
		return i18n.New("正在申请证书，等一下", "A certificate is being issued; please wait")
	}
	d.issuing = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.issuing = false
		d.mu.Unlock()
	}()
	if !force {
		if err := d.selfCheck(ctx, name); err != nil {
			return err
		}
	}
	if email = strings.TrimSpace(email); email != "" {
		d.acme.Email = email
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := d.cfg.ManageSync(cctx, []string{name}); err != nil {
		d.fail(err)
		return i18n.Errorf("申请证书失败：%v", "Couldn't get a certificate: %v", err)
	}
	d.mu.Lock()
	d.data.Domain, d.data.BoundAt, d.data.LastError, d.data.ErrorAt = name, time.Now(), "", time.Time{}
	if email != "" {
		d.data.Email = email
	}
	d.save()
	d.mu.Unlock()
	return nil
}

func (d *domainManager) unbind() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.data.Domain, d.data.BoundAt, d.data.LastError, d.data.ErrorAt = "", time.Time{}, "", time.Time{}
	d.save()
}

func (d *domainManager) renew(ctx context.Context) error {
	name := d.domain()
	if name == "" {
		return i18n.New("还没绑域名", "No domain is bound yet")
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := d.cfg.RenewCertSync(cctx, name, true); err != nil {
		d.fail(err)
		return i18n.Errorf("续期失败：%v", "Renewal failed: %v", err)
	}
	return nil
}

// status 给面板：域名、证书状态（none 没绑 / issuing 正在签 / ok / expiring 30 天内到期 / error 签不下来）、到期时间、上次失败。
func (d *domainManager) status() map[string]any {
	d.mu.Lock()
	data, issuing := d.data, d.issuing
	d.mu.Unlock()
	out := map[string]any{"domain": data.Domain, "status": "none", "email": data.Email, "last_error": data.LastError}
	if !data.ErrorAt.IsZero() {
		out["error_at"] = data.ErrorAt
	}
	if issuing {
		out["status"] = "issuing"
	}
	if data.Domain == "" {
		return out
	}
	c := d.cert(data.Domain)
	switch {
	case c != nil:
		out["not_after"], out["issuer"] = c.NotAfter, c.Issuer.CommonName
		out["days_left"] = int(time.Until(c.NotAfter).Hours() / 24)
		out["status"] = "ok"
		if time.Until(c.NotAfter) < 30*24*time.Hour {
			out["status"] = "expiring"
		}
		if data.LastError != "" && time.Until(c.NotAfter) < 30*24*time.Hour {
			out["status"] = "error" // 快到期了、续期又失败：要人管
		}
	case data.LastError != "":
		out["status"] = "error"
	}
	return out
}

// apiDomain：设置 → 域名（服务端部署、管理员）。GET 状态；PUT { domain, email, force } 绑定并签证书；DELETE 解绑；POST renew 立刻续期。
func (s *Server) apiDomain(w http.ResponseWriter, r *http.Request, rest string) {
	if s.domains == nil {
		fail(w, http.StatusNotFound, i18n.New("域名和证书只在服务端部署时由 Annulo 管理", "Domains and certificates are only managed by Annulo in server deployments"))
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
	case rest == "" && r.Method == http.MethodPut:
		var in struct {
			Domain, Email string
			Force         bool
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if err := s.domains.bind(r.Context(), in.Domain, in.Email, in.Force); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	case rest == "" && r.Method == http.MethodDelete:
		s.domains.unbind()
	case rest == "renew" && r.Method == http.MethodPost:
		if err := s.domains.renew(r.Context()); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
	default:
		fail(w, http.StatusNotFound, fmt.Errorf("no such endpoint: settings/domain/%s", rest))
		return
	}
	st := s.domains.status()
	st["https"] = s.cfg.TLSPort > 0
	writeJSON(w, st)
}

// TLSConfig 是服务端部署 HTTPS 监听的证书配置（app 用）；本机版是 nil。
func (s *Server) TLSConfig() *tls.Config {
	if s.domains == nil {
		return nil
	}
	return s.domains.TLSConfig()
}

// HTTPHandler 是 HTTP 监听用的（服务端部署多了 ACME 验证和跳 https）；HTTPS 监听直接用 Handler。
func (s *Server) HTTPHandler() http.Handler {
	if s.domains == nil {
		return s.Handler()
	}
	return s.domains.HTTPHandler(s.Handler())
}

// certWarning：给管理员顶栏的提醒——证书签不下来、快到期（14 天内）；用 IP 打开、还没绑域名时提醒去绑（登录密码在走明文）。
func (s *Server) certWarning(r *http.Request) map[string]any {
	if s.domains == nil {
		return nil
	}
	if u := userOf(r.Context()); u == nil || u.Role != "admin" {
		return nil
	}
	st := s.domains.status()
	switch {
	case st["domain"] == "":
		if r.TLS == nil {
			return map[string]any{"kind": "no_domain"}
		}
	case st["status"] == "error":
		return map[string]any{"kind": "error", "domain": st["domain"], "message": st["last_error"]}
	case st["status"] == "expiring":
		if d, ok := st["days_left"].(int); ok && d < 14 {
			return map[string]any{"kind": "expiring", "domain": st["domain"], "days_left": d}
		}
	}
	return nil
}
