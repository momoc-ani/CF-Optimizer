import { describe, expect, it } from 'vitest';
import type { DomainDiscovery, PhysicalPath, RouteTransaction } from '../api/types';
import { findVerifiedDomainRoute, isDomainAccelerated } from './domainAcceleration';

const path: PhysicalPath = {
  interface: '以太网 3',
  interface_index: 27,
  source_ipv4: ['192.168.15.116'],
  gateway_ipv4: '192.168.15.1',
};

const route: RouteTransaction = {
  id: 'route-domain',
  operation: 'replace',
  route: { prefix: '104.21.94.176/32', gateway: '192.168.15.1', interface: '以太网 3', interface_index: 27, metric: 5 },
  temporary: false,
  state: 'verified',
  started_at: '2026-08-02T00:00:00Z',
  updated_at: '2026-08-02T00:00:01Z',
  verification: { prefix: '104.21.94.176/32', gateway: '192.168.15.1', interface: '以太网 3', interface_index: 27, metric: 5, source_address: '192.168.15.116' },
};

const domain: DomainDiscovery = {
  domain: 'ani.momoc.top',
  source: 'manual',
  first_seen_at: '2026-08-02T00:00:00Z',
  last_seen_at: '2026-08-02T00:00:01Z',
  cloudflare_verified: true,
  preflight_verified: true,
  active: true,
  accelerated_addresses: ['104.21.94.176'],
  verified_adapters: ['generic-route', 'mihomo'],
};

describe('domainAcceleration', () => {
  it('全链路证据一致时才标记域名已加速', () => {
    expect(findVerifiedDomainRoute('104.21.94.176', [route], path)?.id).toBe(route.id);
    expect(isDomainAccelerated(domain, [route], path)).toBe(true);
  });

  it('网关或代理策略证据缺失时保持未验证', () => {
    const wrongGateway = { ...route, verification: { ...route.verification!, gateway: '192.168.15.254' } };
    expect(isDomainAccelerated(domain, [wrongGateway], path)).toBe(false);
    expect(isDomainAccelerated({ ...domain, verified_adapters: [] }, [route], path)).toBe(false);
  });
});
