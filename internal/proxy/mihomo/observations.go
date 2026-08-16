package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"gopkg.in/yaml.v3"
)

// ConnectionObservation 是从控制 API 提取且不包含节点、认证或完整连接隐私的域名证据。
type ConnectionObservation struct {
	Host          string `json:"host"`
	DestinationIP string `json:"destination_ip,omitempty"`
}

// ObserveConnections 返回当前活动连接中的精确主机名和目标地址。
func (a *Adapter) ObserveConnections(ctx context.Context) ([]ConnectionObservation, error) {
	body, status, err := a.request(ctx, http.MethodGet, "/connections", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Mihomo connections endpoint returned %d", status)
	}
	var response struct {
		Connections []struct {
			Metadata struct {
				Host          string `json:"host"`
				DestinationIP string `json:"destinationIP"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode Mihomo connections: %w", err)
	}
	seen := map[string]struct{}{}
	result := make([]ConnectionObservation, 0, len(response.Connections))
	for _, connection := range response.Connections {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(connection.Metadata.Host)), ".")
		key := host + "\x00" + connection.Metadata.DestinationIP
		if host == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ConnectionObservation{Host: host, DestinationIP: connection.Metadata.DestinationIP})
	}
	return result, nil
}

// CloseDomainConnections 仅关闭 Mihomo 当前连接中主机名精确匹配的连接，避免规则写入后继续沿用旧代理链。
func (a *Adapter) CloseDomainConnections(ctx context.Context, receipt proxy.Receipt, domains []string) (proxy.DomainConnectionCleanupResult, error) {
	allowed := make(map[string]struct{}, len(domains))
	for _, rawDomain := range domains {
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rawDomain)), ".")
		if domain != "" {
			allowed[domain] = struct{}{}
		}
	}
	if receipt.Changed {
		var payload receiptPayload
		if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
			return proxy.DomainConnectionCleanupResult{}, fmt.Errorf("decode Mihomo receipt for connection cleanup: %w", err)
		}
		if payload.PreviousExists && len(payload.Previous) > 0 {
			var previous struct {
				Payload []string `yaml:"payload"`
			}
			if err := yaml.Unmarshal(payload.Previous, &previous); err != nil {
				return proxy.DomainConnectionCleanupResult{}, fmt.Errorf("decode Mihomo previous rules for connection cleanup: %w", err)
			}
			for _, rule := range previous.Payload {
				parts := strings.Split(rule, ",")
				if len(parts) < 3 || !strings.EqualFold(strings.TrimSpace(parts[0]), "DOMAIN") {
					continue
				}
				domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parts[1])), ".")
				if domain != "" {
					allowed[domain] = struct{}{}
				}
			}
		}
	}
	if len(allowed) == 0 {
		return proxy.DomainConnectionCleanupResult{}, nil
	}
	body, status, err := a.request(ctx, http.MethodGet, "/connections", nil)
	if err != nil {
		return proxy.DomainConnectionCleanupResult{}, fmt.Errorf("read Mihomo connections for cleanup: %w", err)
	}
	if status != http.StatusOK {
		return proxy.DomainConnectionCleanupResult{}, fmt.Errorf("Mihomo connections endpoint returned %d", status)
	}
	var response struct {
		Connections []struct {
			ID       string `json:"id"`
			Metadata struct {
				Host string `json:"host"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return proxy.DomainConnectionCleanupResult{}, fmt.Errorf("decode Mihomo connections for cleanup: %w", err)
	}
	result := proxy.DomainConnectionCleanupResult{}
	var cleanupErrors []error
	for _, connection := range response.Connections {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(connection.Metadata.Host)), ".")
		if _, match := allowed[host]; !match || strings.TrimSpace(connection.ID) == "" {
			continue
		}
		result.Matched++
		connectionID := strings.TrimSpace(connection.ID)
		if strings.ContainsAny(connectionID, "/\\?#%\x00\r\n") {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("refuse unsafe Mihomo connection id for %s", host))
			continue
		}
		endpoint := "/connections/" + connectionID
		_, deleteStatus, deleteErr := a.request(ctx, http.MethodDelete, endpoint, nil)
		if deleteErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("close Mihomo connection for %s: %w", host, deleteErr))
			continue
		}
		// 404 表示连接在查询和删除之间已自行结束，视为清理完成。
		if deleteStatus == http.StatusNotFound || (deleteStatus >= 200 && deleteStatus < 300) {
			result.Closed++
			continue
		}
		cleanupErrors = append(cleanupErrors, fmt.Errorf("close Mihomo connection for %s returned %d", host, deleteStatus))
	}
	return result, errors.Join(cleanupErrors...)
}
