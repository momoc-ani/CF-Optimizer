package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
