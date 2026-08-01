package network

import (
	"reflect"
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
