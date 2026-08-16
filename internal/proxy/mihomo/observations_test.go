package mihomo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

// TestCloseDomainConnectionsOnlyClosesExactHosts 验证旧连接清理不会扩展到其他域名或子域名。
func TestCloseDomainConnectionsOnlyClosesExactHosts(t *testing.T) {
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/connections":
			_ = json.NewEncoder(response).Encode(map[string]any{"connections": []map[string]any{
				{"id": "target-1", "metadata": map[string]string{"host": "Comfy.Momoc.Top."}},
				{"id": "target-2", "metadata": map[string]string{"host": "comfy.momoc.top"}},
				{"id": "unrelated", "metadata": map[string]string{"host": "ani.momoc.top"}},
				{"id": "subdomain", "metadata": map[string]string{"host": "asset.comfy.momoc.top"}},
				{"id": "", "metadata": map[string]string{"host": "comfy.momoc.top"}},
			}})
		case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/connections/"):
			deleted = append(deleted, strings.TrimPrefix(request.URL.Path, "/connections/"))
			response.WriteHeader(http.StatusNoContent)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := config.Default().Proxy.Mihomo
	cfg.Controller = server.URL
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.CloseDomainConnections(context.Background(), proxy.Receipt{}, []string{" COMFY.MOMOC.TOP. ", "comfy.momoc.top"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 2 || result.Closed != 2 || !slices.Equal(deleted, []string{"target-1", "target-2"}) {
		t.Fatalf("unexpected domain cleanup: result=%#v deleted=%v", result, deleted)
	}
}

// TestCloseDomainConnectionsContinuesAfterDeleteFailure 验证单条连接关闭失败不会阻止其余匹配连接清理。
func TestCloseDomainConnectionsContinuesAfterDeleteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/connections":
			_ = json.NewEncoder(response).Encode(map[string]any{"connections": []map[string]any{
				{"id": "failed", "metadata": map[string]string{"host": "comfy.momoc.top"}},
				{"id": "closed", "metadata": map[string]string{"host": "comfy.momoc.top"}},
			}})
		case request.Method == http.MethodDelete && request.URL.Path == "/connections/failed":
			response.WriteHeader(http.StatusInternalServerError)
		case request.Method == http.MethodDelete && request.URL.Path == "/connections/closed":
			response.WriteHeader(http.StatusNoContent)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := config.Default().Proxy.Mihomo
	cfg.Controller = server.URL
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.CloseDomainConnections(context.Background(), proxy.Receipt{}, []string{"comfy.momoc.top"})
	if err == nil || result.Matched != 2 || result.Closed != 1 {
		t.Fatalf("partial cleanup was not reported: result=%#v error=%v", result, err)
	}
}

// TestCloseDomainConnectionsIncludesDomainsRemovedFromTheNewPolicy 验证删除最后一个域名时仍会读取旧受管规则并清理连接。
func TestCloseDomainConnectionsIncludesDomainsRemovedFromTheNewPolicy(t *testing.T) {
	deleted := ""
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/connections":
			_ = json.NewEncoder(response).Encode(map[string]any{"connections": []map[string]any{{
				"id": "removed", "metadata": map[string]string{"host": "comfy.momoc.top"},
			}}})
		case request.Method == http.MethodDelete:
			deleted = request.URL.Path
			response.WriteHeader(http.StatusNoContent)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	previous, err := json.Marshal(map[string]any{"payload": []string{"DOMAIN,comfy.momoc.top,DIRECT"}})
	if err != nil {
		t.Fatal(err)
	}
	receiptPayload, err := json.Marshal(receiptPayload{PreviousExists: true, Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Proxy.Mihomo
	cfg.Controller = server.URL
	adapter, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.CloseDomainConnections(context.Background(), proxy.Receipt{Changed: true, Payload: receiptPayload}, nil)
	if err != nil || result.Matched != 1 || result.Closed != 1 || deleted != "/connections/removed" {
		t.Fatalf("removed domain connection was not cleaned: result=%#v deleted=%q error=%v", result, deleted, err)
	}
}
