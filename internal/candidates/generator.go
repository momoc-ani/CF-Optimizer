package candidates

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net/netip"
	"sort"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/ranges"
)

// Options 定义一次候选生成的地址族、数量、种子和历史优先项。
type Options struct {
	Count     int
	IPv4      bool
	IPv6      bool
	Seed      string
	Preferred []netip.Addr
	Cooldown  []netip.Addr
}

// DailySeed 将 UTC 日期与用户种子组合，保证同一天输出可复现。
func DailySeed(now time.Time, custom string) string {
	return now.UTC().Format("2006-01-02") + ":" + custom
}

// Generate 按 IPv4 /24 分片和 IPv6 确定性随机策略生成去重候选。
func Generate(snapshot ranges.Snapshot, options Options) ([]netip.Addr, error) {
	if options.Count < 1 {
		return nil, fmt.Errorf("candidate count must be positive")
	}
	prefixes, err := snapshot.Prefixes(options.IPv4, options.IPv6)
	if err != nil {
		return nil, err
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("no ranges for enabled address families")
	}
	excluded := snapshot.ExcludedPrefixes()
	digest := sha256.Sum256([]byte(options.Seed))
	rng := rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(digest[:8]))))
	seen := make(map[netip.Addr]struct{}, options.Count)
	cooldown := make(map[netip.Addr]struct{}, len(options.Cooldown))
	for _, addr := range options.Cooldown {
		cooldown[addr.Unmap()] = struct{}{}
	}
	result := make([]netip.Addr, 0, options.Count)
	allowed := func(addr netip.Addr, isPreferred bool) bool {
		if !addr.IsValid() || (addr.Is4() && !options.IPv4) || (addr.Is6() && !options.IPv6) {
			return false
		}
		inside := false
		for _, prefix := range prefixes {
			if prefix.Contains(addr) {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
		for _, prefix := range excluded {
			if prefix.Contains(addr) {
				return false
			}
		}
		if _, isCoolingDown := cooldown[addr.Unmap()]; isCoolingDown && !isPreferred {
			return false
		}
		return true
	}
	add := func(addr netip.Addr, isPreferred bool) {
		addr = addr.Unmap()
		if len(result) >= options.Count || !allowed(addr, isPreferred) {
			return
		}
		if _, ok := seen[addr]; ok {
			return
		}
		seen[addr] = struct{}{}
		result = append(result, addr)
	}
	for _, addr := range options.Preferred {
		add(addr, true)
	}

	v4, v6 := split(prefixes)
	v4Target, v6Target := familyTargets(options.Count-len(result), len(v4) > 0, len(v6) > 0)
	v4Target += countFamily(result, true)
	v6Target += countFamily(result, false)
	maxAttempts := options.Count*50 + 1000
	for attempts := 0; len(result) < options.Count && attempts < maxAttempts; attempts++ {
		needV4 := countFamily(result, true) < v4Target
		needV6 := countFamily(result, false) < v6Target
		if needV4 && (!needV6 || attempts%2 == 0) {
			add(sampleIPv4(rng, v4), false)
		} else if needV6 {
			add(sampleIPv6(rng, v6), false)
		} else if len(v4) > 0 {
			add(sampleIPv4(rng, v4), false)
		} else {
			add(sampleIPv6(rng, v6), false)
		}
	}
	if len(result) < options.Count {
		return nil, fmt.Errorf("generated %d of %d candidates after exclusions", len(result), options.Count)
	}
	return result, nil
}

func split(prefixes []netip.Prefix) (v4, v6 []netip.Prefix) {
	for _, prefix := range prefixes {
		if prefix.Addr().Is4() {
			v4 = append(v4, prefix)
		} else {
			v6 = append(v6, prefix)
		}
	}
	return v4, v6
}

func familyTargets(count int, hasV4, hasV6 bool) (int, int) {
	if hasV4 && hasV6 {
		return (count + 1) / 2, count / 2
	}
	if hasV4 {
		return count, 0
	}
	return 0, count
}

func countFamily(values []netip.Addr, ipv4 bool) int {
	count := 0
	for _, value := range values {
		if value.Is4() == ipv4 {
			count++
		}
	}
	return count
}

func sampleIPv4(rng *rand.Rand, prefixes []netip.Prefix) netip.Addr {
	if len(prefixes) == 0 {
		return netip.Addr{}
	}
	weights := make([]uint64, len(prefixes))
	var total uint64
	for i, prefix := range prefixes {
		bits := prefix.Bits()
		weight := uint64(1)
		if bits < 24 {
			weight = uint64(1) << uint(24-bits)
		}
		total += weight
		weights[i] = total
	}
	pick := uint64(rng.Int63n(int64(total)))
	index := sort.Search(len(weights), func(i int) bool { return weights[i] > pick })
	prefix := prefixes[index].Masked()
	baseBytes := prefix.Addr().As4()
	base := binary.BigEndian.Uint32(baseBytes[:])
	bits := prefix.Bits()
	if bits <= 24 {
		chunks := uint32(1) << uint(24-bits)
		chunk := uint32(rng.Int63n(int64(chunks)))
		return uint32Addr(base + chunk*256 + uint32(1+rng.Intn(254)))
	}
	size := uint32(1) << uint(32-bits)
	if size <= 2 {
		return uint32Addr(base + uint32(rng.Intn(int(size))))
	}
	return uint32Addr(base + uint32(1+rng.Intn(int(size-2))))
}

func uint32Addr(value uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], value)
	return netip.AddrFrom4(b)
}

func sampleIPv6(rng *rand.Rand, prefixes []netip.Prefix) netip.Addr {
	if len(prefixes) == 0 {
		return netip.Addr{}
	}
	prefix := prefixes[rng.Intn(len(prefixes))].Masked()
	b := prefix.Addr().As16()
	bits := prefix.Bits()
	for i := bits / 8; i < len(b); i++ {
		b[i] = byte(rng.Intn(256))
	}
	if remainder := bits % 8; remainder != 0 {
		mask := byte(0xff << uint(8-remainder))
		base := prefix.Addr().As16()
		b[bits/8] = (base[bits/8] & mask) | (b[bits/8] & ^mask)
	}
	return netip.AddrFrom16(b)
}
