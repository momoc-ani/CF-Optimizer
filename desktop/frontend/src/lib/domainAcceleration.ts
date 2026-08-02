import type { DomainDiscovery, PhysicalPath, RouteTransaction } from '../api/types';

/** findVerifiedDomainRoute 要求事务中的目标、接口、网关和源地址均与物理出口一致。 */
export function findVerifiedDomainRoute(address: string, routes: RouteTransaction[], path?: PhysicalPath): RouteTransaction | undefined {
  if (!path?.interface || !address) return undefined;
  const isIPv6 = address.includes(':');
  const prefix = `${address}/${isIPv6 ? 128 : 32}`;
  const gateway = isIPv6 ? path.gateway_ipv6 : path.gateway_ipv4;
  const sources = isIPv6 ? path.source_ipv6 : path.source_ipv4;
  if (!gateway || !sources?.length) return undefined;

  return routes.find((transaction) => {
    const observed = transaction.verification;
    if (transaction.state !== 'verified' || transaction.route.prefix !== prefix || !observed) return false;
    const expectedInterfaceMatches = path.interface_index
      ? transaction.route.interface_index === path.interface_index
      : transaction.route.interface === path.interface;
    const observedInterfaceMatches = path.interface_index
      ? observed.interface_index === path.interface_index
      : observed.interface === path.interface;
    return expectedInterfaceMatches
      && observedInterfaceMatches
      && transaction.route.gateway === gateway
      && observed.gateway === gateway
      && Boolean(observed.source_address && sources.includes(observed.source_address));
  });
}

/** isDomainAccelerated 仅在域名、策略、映射和全部物理路由证据完整时返回 true。 */
export function isDomainAccelerated(domain: DomainDiscovery, routes: RouteTransaction[], path?: PhysicalPath): boolean {
  const addresses = domain.accelerated_addresses ?? [];
  return domain.cloudflare_verified
    && domain.preflight_verified
    && domain.active
    && addresses.length > 0
    && Boolean(domain.verified_adapters?.length)
    && addresses.every((address) => Boolean(findVerifiedDomainRoute(address, routes, path)));
}
