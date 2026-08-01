//go:build linux

package network

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type linuxDefaultRoute struct {
	interfaceName string
	gateway       string
	metric        int
}

func discoverPlatformPath(_ context.Context, interfaceOverride string, _ time.Duration) (PhysicalPath, error) {
	v4Routes, v4Err := readLinuxIPv4Defaults()
	v6Routes, v6Err := readLinuxIPv6Defaults()
	if v4Err != nil && v6Err != nil {
		return PhysicalPath{}, fmt.Errorf("read Linux default routes: IPv4: %v; IPv6: %v", v4Err, v6Err)
	}
	all := append(v4Routes, v6Routes...)
	selectedInterface := interfaceOverride
	if selectedInterface == "" {
		for _, route := range all {
			iface, err := net.InterfaceByName(route.interfaceName)
			if err == nil && !IsLikelyVirtual(*iface) {
				selectedInterface = route.interfaceName
				break
			}
		}
	}
	path := PhysicalPath{Interface: selectedInterface}
	for _, route := range v4Routes {
		if route.interfaceName == selectedInterface {
			path.GatewayIPv4 = route.gateway
			break
		}
	}
	for _, route := range v6Routes {
		if route.interfaceName == selectedInterface {
			path.GatewayIPv6 = route.gateway
			break
		}
	}
	return path, nil
}

func readLinuxIPv4Defaults() ([]linuxDefaultRoute, error) {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var routes []linuxDefaultRoute
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[1] != "00000000" {
			continue
		}
		flags, flagErr := strconv.ParseUint(fields[3], 16, 32)
		metric, metricErr := strconv.Atoi(fields[6])
		gatewayBytes, gatewayErr := hex.DecodeString(fields[2])
		if flagErr != nil || metricErr != nil || gatewayErr != nil || len(gatewayBytes) != 4 || flags&0x1 == 0 {
			continue
		}
		gateway := netip.AddrFrom4([4]byte{gatewayBytes[3], gatewayBytes[2], gatewayBytes[1], gatewayBytes[0]})
		routes = append(routes, linuxDefaultRoute{interfaceName: fields[0], gateway: gateway.String(), metric: metric})
	}
	sortLinuxRoutes(routes)
	return routes, scanner.Err()
}

func readLinuxIPv6Defaults() ([]linuxDefaultRoute, error) {
	file, err := os.Open("/proc/net/ipv6_route")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var routes []linuxDefaultRoute
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[0] != strings.Repeat("0", 32) || fields[1] != "00" {
			continue
		}
		gatewayBytes, gatewayErr := hex.DecodeString(fields[4])
		metric, metricErr := strconv.ParseUint(fields[5], 16, 32)
		if gatewayErr != nil || metricErr != nil || len(gatewayBytes) != 16 {
			continue
		}
		var rawGateway [16]byte
		copy(rawGateway[:], gatewayBytes)
		gateway := netip.AddrFrom16(rawGateway)
		routes = append(routes, linuxDefaultRoute{interfaceName: fields[9], gateway: gateway.String(), metric: int(metric)})
	}
	sortLinuxRoutes(routes)
	return routes, scanner.Err()
}

func sortLinuxRoutes(routes []linuxDefaultRoute) {
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].metric < routes[j].metric })
}
