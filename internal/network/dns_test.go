package network

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeDNSServersFiltersInvalidAndDuplicateAddresses(t *testing.T) {
	actual := normalizeDNSServers([]string{
		"192.168.15.1", "192.168.15.1", "::ffff:192.168.15.1", "fdb5:b32f:4e55::1", "", "not-an-ip", "0.0.0.0", "ff02::1",
	})
	expected := []string{"192.168.15.1", "fdb5:b32f:4e55::1"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("unexpected normalized DNS servers: %#v", actual)
	}
}

func TestParseResolvConf(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name: "comments and options",
			content: "# generated\nsearch example.test\nnameserver 192.0.2.53 # primary\n" +
				"options timeout:2\nnameserver 2001:db8::53\n",
			expected: []string{"192.0.2.53", "2001:db8::53"},
		},
		{
			name: "invalid and duplicate entries",
			content: "nameserver invalid\nnameserver 192.0.2.53\nnameserver 192.0.2.53\n" +
				"nameserver ::ffff:192.0.2.53\nnameserver 0.0.0.0\n",
			expected: []string{"192.0.2.53"},
		},
		{name: "empty", content: "# no resolver\n", expected: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := parseResolvConf(strings.NewReader(test.content))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, test.expected) {
				t.Fatalf("unexpected resolv.conf servers: got %#v, want %#v", actual, test.expected)
			}
		})
	}
}
