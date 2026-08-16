import type { AppConfig, DomainDiscovery, PhysicalPath, RouteTransaction } from '../api/types';

function routeInterfaceMatches(route: RouteTransaction['route'], path: PhysicalPath): boolean {
  if (route.interface_index && path.interface_index) return route.interface_index === path.interface_index;
  return Boolean(route.interface && route.interface === path.interface);
}

/** findVerifiedDomainRoute 校验平台能够返回的接口、网关和可选源地址证据。 */
export function findVerifiedDomainRoute(address: string, routes: RouteTransaction[], path?: PhysicalPath): RouteTransaction | undefined {
  if (!path?.interface || !address) return undefined;
  const isIPv6 = address.includes(':');
  const prefix = `${address}/${isIPv6 ? 128 : 32}`;
  const gateway = isIPv6 ? path.gateway_ipv6 : path.gateway_ipv4;
  const sources = isIPv6 ? path.source_ipv6 : path.source_ipv4;
  if (!gateway) return undefined;

  return routes.find((transaction) => {
    const observed = transaction.verification;
    if (transaction.state !== 'verified' || transaction.route.prefix !== prefix || !observed) return false;
    const sourceMatches = !observed.source_address || Boolean(sources?.includes(observed.source_address));
    return routeInterfaceMatches(transaction.route, path)
      && routeInterfaceMatches(observed, path)
      && transaction.route.gateway === gateway
      && observed.gateway === gateway
      && sourceMatches;
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

/** filterUsableResolvedAddresses 隐藏当前策略未启用或缺少受管物理网关的历史 DNS 地址族。 */
export function filterUsableResolvedAddresses(addresses: string[] = [], config?: AppConfig, path?: PhysicalPath): string[] {
  if (!config) return addresses;
  const requiresGateway = config.network.manage_routes;
  const allowIPv4 = config.benchmark.ipv4 && (!requiresGateway || Boolean(path?.gateway_ipv4));
  const allowIPv6 = config.benchmark.ipv6 && (!requiresGateway || Boolean(path?.gateway_ipv6));
  return addresses.filter((address) => address.includes(':') ? allowIPv6 : allowIPv4);
}
