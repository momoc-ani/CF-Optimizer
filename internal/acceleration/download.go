package acceleration

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"golang.org/x/net/html"
)

const (
	// DefaultDomainDownloadMaxBytes 限制每个候选的域名复测流量为 1 MiB。
	DefaultDomainDownloadMaxBytes   int64 = 1 << 20
	domainDiscoveryMaxBody                = 256 << 10
	domainMinimumProbeBytes         int64 = 64 << 10
	domainMaximumResourceCandidates       = 8
)

// DownloadOptions 定义手动域名资源发现和单候选下载复测边界。
type DownloadOptions struct {
	DiscoveryTimeout time.Duration
	DownloadTimeout  time.Duration
	MaxBytes         int64
}

// DownloadResult 保存目标域名在指定候选地址上的实际下载指标。
type DownloadResult struct {
	ProbeURL   string        `json:"probe_url"`
	Downloaded int64         `json:"downloaded"`
	Duration   time.Duration `json:"duration"`
	Mbps       float64       `json:"mbps"`
}

// DownloadTester 使用目标域名的 SNI、Host 和同域资源执行直连下载复测。
type DownloadTester struct {
	dial             cfnetwork.DialContextFunc
	discoveryTimeout time.Duration
	downloadTimeout  time.Duration
	maxBytes         int64
	rootCAs          *x509.CertPool
	now              func() time.Time
}

// NewDownloadTester 创建不读取代理环境变量的手动域名下载复测器。
func NewDownloadTester(dial cfnetwork.DialContextFunc, options DownloadOptions) (*DownloadTester, error) {
	if dial == nil || options.DiscoveryTimeout <= 0 || options.DownloadTimeout <= 0 {
		return nil, errors.New("domain download tester requires a dialer and positive timeouts")
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultDomainDownloadMaxBytes
	}
	return &DownloadTester{
		dial: dial, discoveryTimeout: options.DiscoveryTimeout, downloadTimeout: options.DownloadTimeout,
		maxBytes: options.MaxBytes, now: time.Now,
	}, nil
}

// DiscoverProbeURL 从候选地址承载的域名首页中选择足够大的同域静态资源。
func (t *DownloadTester) DiscoverProbeURL(ctx context.Context, domain, rawAddress string) (string, error) {
	discoveryContext, cancel := context.WithTimeout(ctx, t.discoveryTimeout)
	defer cancel()
	rootURL := &url.URL{Scheme: "https", Host: domain, Path: "/"}
	body, contentType, contentLength, err := t.readResource(discoveryContext, domain, rawAddress, rootURL, "", domainDiscoveryMaxBody)
	if err != nil {
		return "", fmt.Errorf("read domain probe page: %w", err)
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if !strings.EqualFold(mediaType, "text/html") {
		if contentLength >= domainMinimumProbeBytes {
			return rootURL.String(), nil
		}
		return "", fmt.Errorf("domain root has no HTML resource list and is only %d bytes", contentLength)
	}
	resources, err := sameOriginResources(rootURL, domain, body)
	if err != nil {
		return "", err
	}
	if len(resources) == 0 {
		return "", errors.New("domain page has no same-origin downloadable resource")
	}
	var selected *url.URL
	var selectedLength int64
	for index, resource := range resources {
		if index >= domainMaximumResourceCandidates {
			break
		}
		length, inspectErr := t.inspectResource(discoveryContext, domain, rawAddress, resource)
		if inspectErr != nil || length < domainMinimumProbeBytes {
			continue
		}
		if selected == nil || length > selectedLength {
			selected = resource
			selectedLength = length
		}
	}
	if selected == nil {
		return "", fmt.Errorf("domain page has no same-origin resource of at least %d bytes", domainMinimumProbeBytes)
	}
	return selected.String(), nil
}

// Measure 下载同域资源的受限字节范围，并返回包含首字节等待的实际 Mbps。
func (t *DownloadTester) Measure(ctx context.Context, domain, rawAddress, rawProbeURL string) (DownloadResult, error) {
	probeURL, err := validateProbeURL(domain, rawProbeURL)
	if err != nil {
		return DownloadResult{}, err
	}
	downloadContext, cancel := context.WithTimeout(ctx, t.downloadTimeout)
	defer cancel()
	connection, err := t.openTLS(downloadContext, domain, rawAddress)
	if err != nil {
		return DownloadResult{}, err
	}
	defer connection.Close()
	started := t.now()
	rangeHeader := fmt.Sprintf("bytes=0-%d", t.maxBytes-1)
	response, err := writeDomainRequest(connection, domain, probeURL, rangeHeader)
	if err != nil {
		return DownloadResult{}, err
	}
	defer response.Body.Close()
	if err := validateDownloadStatus(response); err != nil {
		return DownloadResult{}, err
	}
	downloaded, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, t.maxBytes))
	duration := t.now().Sub(started)
	if downloaded == 0 {
		if readErr != nil {
			return DownloadResult{}, fmt.Errorf("read domain download body: %w", readErr)
		}
		return DownloadResult{}, errors.New("domain download returned an empty body")
	}
	if readErr != nil && !isTimeout(readErr) {
		return DownloadResult{}, fmt.Errorf("read domain download body: %w", readErr)
	}
	if duration <= 0 {
		return DownloadResult{}, errors.New("domain download duration is invalid")
	}
	return DownloadResult{
		ProbeURL: probeURL.String(), Downloaded: downloaded, Duration: duration,
		Mbps: float64(downloaded*8) / duration.Seconds() / 1_000_000,
	}, nil
}

// readResource 读取受限响应正文，并保留资源类型和可用总长度供资源发现使用。
func (t *DownloadTester) readResource(ctx context.Context, domain, rawAddress string, target *url.URL, rangeHeader string, limit int64) ([]byte, string, int64, error) {
	connection, err := t.openTLS(ctx, domain, rawAddress)
	if err != nil {
		return nil, "", 0, err
	}
	defer connection.Close()
	response, err := writeDomainRequest(connection, domain, target, rangeHeader)
	if err != nil {
		return nil, "", 0, err
	}
	defer response.Body.Close()
	if err := validateDownloadStatus(response); err != nil {
		return nil, "", 0, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, "", 0, err
	}
	return body, response.Header.Get("Content-Type"), responseResourceLength(response), nil
}

// inspectResource 使用单字节 Range 请求确认资源可下载并读取完整长度。
func (t *DownloadTester) inspectResource(ctx context.Context, domain, rawAddress string, target *url.URL) (int64, error) {
	_, _, length, err := t.readResource(ctx, domain, rawAddress, target, "bytes=0-0", 1)
	return length, err
}

// openTLS 连接精确候选地址，同时使用手动域名完成证书验证和 SNI 握手。
func (t *DownloadTester) openTLS(ctx context.Context, domain, rawAddress string) (*tls.Conn, error) {
	address, err := netip.ParseAddr(rawAddress)
	if err != nil {
		return nil, fmt.Errorf("parse domain download address: %w", err)
	}
	rawConnection, err := t.dial(ctx, "tcp", net.JoinHostPort(address.Unmap().String(), "443"))
	if err != nil {
		return nil, fmt.Errorf("dial domain download candidate %s: %w", address, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := rawConnection.SetDeadline(deadline); err != nil {
			rawConnection.Close()
			return nil, fmt.Errorf("set domain download deadline: %w", err)
		}
	}
	tlsConnection := tls.Client(rawConnection, &tls.Config{
		ServerName: domain, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, RootCAs: t.rootCAs,
	})
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		rawConnection.Close()
		return nil, fmt.Errorf("domain download TLS handshake: %w", err)
	}
	return tlsConnection, nil
}

// writeDomainRequest 在既有 TLS 连接上发送不压缩的同域 GET 请求。
func writeDomainRequest(connection net.Conn, domain string, target *url.URL, rangeHeader string) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Host = domain
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Connection", "close")
	request.Header.Set("User-Agent", "CF-Optimizer/1")
	if rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	if err := request.Write(connection); err != nil {
		return nil, fmt.Errorf("write domain download request: %w", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		return nil, fmt.Errorf("read domain download response: %w", err)
	}
	return response, nil
}

// validateDownloadStatus 拒绝非成功响应，并保留 Error 1034 的明确诊断。
func validateDownloadStatus(response *http.Response) error {
	if response == nil || response.Body == nil {
		return errors.New("domain download response body is unavailable")
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, maximumPreflightBody))
	normalized := strings.ToLower(string(body))
	if strings.Contains(normalized, "error 1034") && strings.Contains(normalized, "edge ip restricted") {
		return fmt.Errorf("Cloudflare returned %s with Error 1034 Edge IP Restricted", response.Status)
	}
	return fmt.Errorf("domain download returned %s", response.Status)
}

// responseResourceLength 从 Content-Range 或 Content-Length 中恢复资源总长度。
func responseResourceLength(response *http.Response) int64 {
	if response == nil {
		return 0
	}
	if contentRange := response.Header.Get("Content-Range"); contentRange != "" {
		if slash := strings.LastIndexByte(contentRange, '/'); slash >= 0 {
			if total, err := strconv.ParseInt(strings.TrimSpace(contentRange[slash+1:]), 10, 64); err == nil {
				return total
			}
		}
	}
	if response.ContentLength > 0 {
		return response.ContentLength
	}
	return 0
}

// sameOriginResources 使用 HTML 解析器提取同域脚本、样式、图片和媒体资源。
func sameOriginResources(base *url.URL, domain string, body []byte) ([]*url.URL, error) {
	document, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse domain probe page: %w", err)
	}
	seen := make(map[string]struct{})
	var resources []*url.URL
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode {
			attribute := ""
			switch strings.ToLower(node.Data) {
			case "script", "img", "source", "video":
				attribute = "src"
			case "link":
				attribute = "href"
			}
			if attribute != "" {
				for _, item := range node.Attr {
					if strings.EqualFold(item.Key, attribute) {
						if resource, resolveErr := resolveSameOriginResource(base, domain, item.Val); resolveErr == nil {
							if _, exists := seen[resource.String()]; !exists {
								seen[resource.String()] = struct{}{}
								resources = append(resources, resource)
							}
						}
						break
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return resources, nil
}

// resolveSameOriginResource 只接受与手动域名同源的 HTTPS 资源。
func resolveSameOriginResource(base *url.URL, domain, rawReference string) (*url.URL, error) {
	reference, err := url.Parse(strings.TrimSpace(rawReference))
	if err != nil {
		return nil, err
	}
	resolved := base.ResolveReference(reference)
	if !strings.EqualFold(resolved.Scheme, "https") || !strings.EqualFold(resolved.Hostname(), domain) || resolved.User != nil {
		return nil, errors.New("resource is not same-origin HTTPS")
	}
	if port := resolved.Port(); port != "" && port != "443" {
		return nil, errors.New("resource uses a non-HTTPS port")
	}
	resolved.Fragment = ""
	return resolved, nil
}

// validateProbeURL 校验已发现资源仍属于当前手动域名。
func validateProbeURL(domain, rawProbeURL string) (*url.URL, error) {
	probeURL, err := url.Parse(rawProbeURL)
	if err != nil {
		return nil, fmt.Errorf("parse domain probe URL: %w", err)
	}
	return resolveSameOriginResource(&url.URL{Scheme: "https", Host: domain, Path: "/"}, domain, probeURL.String())
}

// isTimeout 判断读取停止是否由受控测速窗口到期导致。
func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
