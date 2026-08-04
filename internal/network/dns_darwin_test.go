//go:build darwin

package network

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDiscoverDarwinDNSServersFallbackOrder(t *testing.T) {
	path := PhysicalPath{Interface: "en0", InterfaceIndex: 7}
	tests := []struct {
		name          string
		scutilOutput  string
		scutilErr     error
		fileServers   []string
		fileErr       error
		expected      []string
		expectedReads int
		expectsError  bool
	}{
		{
			name:         "interface resolver wins",
			scutilOutput: "resolver #1\n  nameserver[0] : 192.0.2.53\n  if_index : 7 (en0)\n",
			fileServers:  []string{"198.51.100.53"}, expected: []string{"192.0.2.53"},
		},
		{
			name:         "empty interface resolver falls back to resolv conf",
			scutilOutput: "DNS configuration\n\nresolver #1\n  nameserver[0] : 203.0.113.53\n  if_index : 12 (utun3)\n",
			fileServers:  []string{"198.51.100.53", "198.51.100.53"},
			expected:     []string{"198.51.100.53"}, expectedReads: 1,
		},
		{
			name:      "command failure falls back to resolv conf",
			scutilErr: errors.New("scutil failed"), fileServers: []string{"198.51.100.53"},
			expected: []string{"198.51.100.53"}, expectedReads: 1,
		},
		{
			name:         "all sources empty",
			scutilOutput: "No DNS configuration available\n", fileErr: errors.New("missing"),
			expectedReads: 1, expectsError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			readCount := 0
			actual, err := discoverDarwinDNSServers(
				context.Background(), path, time.Second,
				func(context.Context, time.Duration) ([]byte, error) {
					return []byte(test.scutilOutput), test.scutilErr
				},
				func(path string) ([]string, error) {
					readCount++
					if path != darwinResolvConfPath {
						t.Fatalf("unexpected resolv.conf path %q", path)
					}
					return test.fileServers, test.fileErr
				},
			)
			if (err != nil) != test.expectsError {
				t.Fatalf("unexpected error state: %v", err)
			}
			if !reflect.DeepEqual(actual, test.expected) {
				t.Fatalf("unexpected DNS servers: got %#v, want %#v", actual, test.expected)
			}
			if readCount != test.expectedReads {
				t.Fatalf("unexpected resolv.conf read count: got %d, want %d", readCount, test.expectedReads)
			}
		})
	}
}
