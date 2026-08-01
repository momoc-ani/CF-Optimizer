import type { RouteTransaction, ServiceState } from '../api/types';

const temporaryRouteStatesRequiringCleanup = new Set([
  'planned',
  'applied',
  'verified',
  'rollback_failed',
  'recovery_failed',
]);

/** countCurrentVerifiedRoutes 只统计当前节点状态中仍有验证证据的路由。 */
export function countCurrentVerifiedRoutes(state?: Pick<ServiceState, 'current_ipv4' | 'current_ipv6'>): number {
  return [state?.current_ipv4, state?.current_ipv6]
    .filter((selection) => selection?.policy_verified)
    .length;
}

/** countTemporaryRoutesRequiringCleanup 按后台恢复规则统计尚未进入终态的临时路由。 */
export function countTemporaryRoutesRequiringCleanup(
  routes: readonly Pick<RouteTransaction, 'temporary' | 'state'>[],
): number {
  return routes.filter((route) => (
    route.temporary && temporaryRouteStatesRequiringCleanup.has(route.state)
  )).length;
}
