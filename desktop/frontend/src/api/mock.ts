import type {
  AppConfig,
  BenchmarkResult,
  DiagnosticReport,
  DomainDiscovery,
  LatestBenchmark,
  OptimizerEvent,
  ProxyDetections,
  QuickStartPlan,
  QuickStartResult,
  RangeSnapshot,
  RouteTransaction,
  RunReport,
  RunSummary,
  SystemStatus,
} from './types';

const now = new Date('2026-08-01T09:36:00+09:00');
const iso = (offsetMinutes: number) => new Date(now.getTime() + offsetMinutes * 60_000).toISOString();
const latency = (milliseconds: number) => milliseconds * 1_000_000;

const results: BenchmarkResult[] = [
  { ip: '104.16.132.229', family: 4, attempts: 4, successes: 4, loss: 0, avg_latency: latency(31.8), p95_latency: latency(35.2), jitter: latency(2.1), tls_latency: latency(44.6), ttfb: latency(72.4), mbps: 184.7, tcp_qualified: true, tls_verified: true, download_verified: true, qualified: true, score: 94.6 },
  { ip: '2606:4700:3037::6815:1d8', family: 6, attempts: 4, successes: 4, loss: 0, avg_latency: latency(37.1), p95_latency: latency(42.8), jitter: latency(3.4), tls_latency: latency(51.3), ttfb: latency(83.6), mbps: 162.2, tcp_qualified: true, tls_verified: true, download_verified: true, qualified: true, score: 89.8 },
  { ip: '172.66.44.18', family: 4, attempts: 4, successes: 4, loss: 0, avg_latency: latency(42.7), p95_latency: latency(49.9), jitter: latency(4.8), tls_latency: latency(59.1), ttfb: latency(91.5), mbps: 139.4, tcp_qualified: true, tls_verified: true, download_verified: true, qualified: true, score: 84.2 },
  { ip: '104.17.210.9', family: 4, attempts: 4, successes: 3, loss: 0.25, avg_latency: latency(61.2), p95_latency: latency(74.7), jitter: latency(8.6), tls_latency: latency(78.5), ttfb: latency(112.4), mbps: 92.1, tcp_qualified: true, tls_verified: true, download_verified: true, qualified: true, score: 71.5 },
  { ip: '2a06:98c1:3120::4', family: 6, attempts: 4, successes: 2, loss: 0.5, avg_latency: latency(127.4), p95_latency: latency(144.2), jitter: latency(15.1), tcp_qualified: false, tls_verified: false, download_verified: false, qualified: false, score: 0, error: 'loss threshold exceeded' },
];

const history: RunSummary[] = [
  { id: 'run-20260801-0930', started_at: iso(-6), finished_at: iso(-3), candidates: 1000, qualified: 27, best: results.slice(0, 3).map(({ ip, score, avg_latency, loss, mbps }) => ({ ip, score, avg_latency, loss, mbps: mbps ?? 0 })), selected_ipv4: '104.16.132.229', selected_ipv6: '2606:4700:3037::6815:1d8', switch_reason: 'IPv4 improved 18.4%; IPv6 kept by minimum hold' },
  { id: 'run-20260801-0330', started_at: iso(-366), finished_at: iso(-363), candidates: 1000, qualified: 24, selected_ipv4: '172.66.44.18', selected_ipv6: '2606:4700:3037::6815:1d8', switch_reason: 'current nodes remained stable' },
  { id: 'run-20260731-2130', started_at: iso(-726), finished_at: iso(-723), candidates: 1000, qualified: 19, selected_ipv4: '172.66.44.18', switch_reason: 'IPv6 had no qualified candidate', error: 'IPv6 partial failure' },
];

let latestBenchmark: LatestBenchmark = {
  run_id: history[0].id,
  finished_at: history[0].finished_at,
  saved_at: history[0].finished_at,
  results,
};

const manualDomain: DomainDiscovery = { domain: 'ani.momoc.top', source: 'manual', first_seen_at: iso(-60), last_seen_at: iso(0), cloudflare_verified: true, preflight_verified: true, download_verified: true, download_mbps: 86.4, download_address: '104.16.132.229', download_probe_url: 'https://ani.momoc.top/assets/test.bin', download_tested_at: iso(-4), active: true, last_resolved_addresses: ['104.21.92.119', '172.67.192.253'], accelerated_addresses: ['104.16.132.229'], verified_adapters: ['generic-route', 'mihomo', 'windows-hosts'], applied_at: iso(-3) };
let discoveredDomains: DomainDiscovery[] = [
  { domain: 'dash.cloudflare.com', source: 'mihomo', first_seen_at: iso(-45), last_seen_at: iso(-1), cloudflare_verified: true, preflight_verified: true, download_verified: false, active: true, last_resolved_addresses: ['104.16.123.96'], accelerated_addresses: ['172.66.44.18'], verified_adapters: ['generic-route', 'mihomo', 'windows-hosts'], applied_at: iso(-3) },
];

let cancelled = false;
let activeEvent: OptimizerEvent | undefined;
let running = false;
const listeners = new Set<(event: OptimizerEvent) => void>();

const baseStatus = (): SystemStatus => ({
  build: { version: '0.1.0-dev', commit: 'local', date: '2026-08-01' },
  protocol_version: 1,
  state: {
    version: 1,
    updated_at: iso(0),
    current_ipv4: { ip: '104.16.132.229', family: 4, score: 94.6, selected_at: iso(-3), last_successful_at: iso(-3), consecutive_failures: 0, policy_verified: true },
    current_ipv6: { ip: '2606:4700:3037::6815:1d8', family: 6, score: 89.8, selected_at: iso(-363), last_successful_at: iso(-3), consecutive_failures: 0, policy_verified: true },
    last_started_at: iso(-6),
    last_ended_at: iso(-3),
    running,
  },
  schedule: { enabled: true, interval: '6h0m0s', next_scheduled_at: iso(354), trigger: 'interval' },
  policy_guards: {
    mihomo: { id: 'mihomo', state: 'verified', online: true, activity: 'active', system_proxy_active: true, tun_active: false, manageable: true, endpoint: 'http://127.0.0.1:9097', config_path: 'C:\\Users\\demo\\AppData\\Roaming\\Clash Verge\\clash-verge.yaml', last_checked_at: iso(-1), last_verified_at: iso(-2), transition: 3, message: 'Mihomo DIRECT、目标地址和物理出口已验证' },
  },
  physical_path: { interface: 'Ethernet 2', interface_index: 12, source_ipv4: ['192.168.50.24'], source_ipv6: ['2408:8214:1320::24'], gateway_ipv4: '192.168.50.1', gateway_ipv6: 'fe80::1' },
  policy_available: true,
  active_event: activeEvent,
});

export const mockRangeSnapshot: RangeSnapshot = {
  version: 1,
  fetched_at: iso(-48),
  source: 'cloudflare-api',
  etag: 'W/"cf-ips-20260801"',
  hash: '7498fd8cf1742d5ace97b699749eb488df63458f17a6d275b84a47f21ee19f72',
  ipv4: ['173.245.48.0/20', '103.21.244.0/22', '104.16.0.0/13', '104.24.0.0/14', '172.64.0.0/13', '198.41.128.0/17'],
  ipv6: ['2400:cb00::/32', '2606:4700::/32', '2a06:98c0::/29'],
  include: [],
  exclude: ['104.16.10.0/24'],
};

const routes: RouteTransaction[] = [
  { id: 'rt-72d4af2a', operation: 'replace', route: { prefix: '104.16.132.229/32', gateway: '192.168.50.1', interface: 'Ethernet 2', interface_index: 12, metric: 5 }, temporary: false, state: 'verified', started_at: iso(-3), updated_at: iso(-3), verification: { prefix: '104.16.132.229/32', gateway: '192.168.50.1', interface: 'Ethernet 2', interface_index: 12, metric: 5, source_address: '192.168.50.24' } },
  { id: 'rt-dc632b8e', operation: 'replace', route: { prefix: '2606:4700:3037::6815:1d8/128', gateway: 'fe80::1', interface: 'Ethernet 2', interface_index: 12, metric: 5 }, temporary: false, state: 'verified', started_at: iso(-363), updated_at: iso(-3), verification: { prefix: '2606:4700:3037::6815:1d8/128', gateway: 'fe80::1', interface: 'Ethernet 2', interface_index: 12, metric: 5, source_address: '2408:8214:1320::24' } },
];

const proxyDetections: ProxyDetections = {
  generic: { present: true, manageable: true, version: 'route-v1', message: 'Host route lifecycle available' },
  mihomo: { present: true, manageable: true, version: 'v1.19.4', endpoint: 'http://127.0.0.1:9097', config_path: 'C:\\Users\\demo\\AppData\\Roaming\\Clash Verge\\clash-verge.yaml', message: 'Controller and active config reachable' },
  'sing-box': { present: false, manageable: false, message: 'Managed file is not configured' },
  xray: { present: false, manageable: false, message: 'Managed file is not configured' },
};

export const mockConfig: AppConfig = {
  version: 1,
  data_dir: '/var/lib/cf-optimizer',
  schedule: { enabled: true, interval: '6h0m0s', run_on_network_change: true, network_poll: '30s' },
  ranges: { source: 'cloudflare-api', api_url: 'https://api.cloudflare.com/client/v4/ips', ipv4_url: 'https://www.cloudflare.com/ips-v4', ipv6_url: 'https://www.cloudflare.com/ips-v6', refresh_interval: '24h0m0s', stale_after: '168h0m0s', max_change_percent: 30, request_timeout: '20s', include: [], exclude: ['104.16.10.0/24'] },
  benchmark: { ipv4: true, ipv6: true, candidates: 6000, connect_attempts: 4, concurrency: 200, connect_timeout: '1.5s', latency_limit: '300ms', loss_limit: 0.25, download_top: 20, download_concurrency: 5, download_url: 'https://speed.cloudflare.com/__down?bytes=52428800', tls_server_name: 'speed.cloudflare.com', tls_timeout: '5s', download_duration: '8s', download_max_bytes: 52428800, switch_improvement: 0.15, minimum_hold: '30m0s', failure_threshold: 3, failure_cooldown: '6h0m0s', daily_seed: '' },
  network: { interface: 'Ethernet 2', gateway_ipv4: '192.168.50.1', gateway_ipv6: 'fe80::1', manage_routes: true, command_timeout: '10s' },
  acceleration: { enabled: true, manual_domains: ['ani.momoc.top'], manual_mappings: { 'ani.momoc.top': '104.16.132.229' }, excluded_domains: [], manual_download_test: true, manual_download_min_mbps: 20, auto_discover: false, auto_apply: true, discovery_interval: '15s', max_discovered_domains: 1000, apply_verification_timeout: '20s', apply_attempt_timeout: '5s', apply_retry_interval: '500ms', apply_max_attempts: 4 },
  proxy: { auto_detect: true, generic: { enabled: true }, mihomo: { enabled: true, controller: 'http://127.0.0.1:9090', provider_file: '/etc/mihomo/rules/cf-optimizer.yaml', reload_config: '', timeout: '5s' }, sing_box: { enabled: false }, xray: { enabled: false }, external: { enabled: false } },
  hosts: { enabled: false, path: 'C:\\Windows\\System32\\drivers\\etc\\hosts', domains: [] },
  ipc: { endpoint: '\\\\.\\pipe\\cf-optimizer-v1' },
  history: { summary_retention: '720h0m0s', detail_retention: '168h0m0s', max_runs: 500 },
};

function emit(event: OptimizerEvent) {
  activeEvent = event;
  listeners.forEach((listener) => listener(event));
}

async function runOptimization(shouldHoldForCancellation = false): Promise<RunReport> {
  if (running) throw new Error('conflict: an optimization run is already active');
  running = true;
  cancelled = false;
  const runId = `run-${Date.now()}`;
  const stages: Array<OptimizerEvent & { delay: number }> = [
    { run_id: runId, type: 'run.started', stage: 'ranges', message: '正在更新网段', timestamp: new Date().toISOString(), delay: 140 },
    { run_id: runId, type: 'benchmark.progress', stage: 'tcp', progress: { stage: 'tcp', completed: 260, total: 1000, qualified: 18, ip: '104.16.132.229' }, timestamp: new Date().toISOString(), delay: 180 },
    { run_id: runId, type: 'benchmark.progress', stage: 'tcp', progress: { stage: 'tcp', completed: 1000, total: 1000, qualified: 27, ip: '172.66.44.18' }, timestamp: new Date().toISOString(), delay: 180 },
    { run_id: runId, type: 'benchmark.progress', stage: 'download', progress: { stage: 'download', completed: 12, total: 20, qualified: 27, ip: '104.16.132.229' }, timestamp: new Date().toISOString(), delay: 220 },
    { run_id: runId, type: 'selection.completed', stage: 'selection', message: 'IPv4 improved 18.4%; IPv6 kept', timestamp: new Date().toISOString(), delay: 180 },
  ];
  for (const [index, stage] of stages.entries()) {
    await new Promise((resolve) => window.setTimeout(resolve, stage.delay));
    if (cancelled) {
      running = false;
      activeEvent = undefined;
      throw new Error('cancelled: optimization was cancelled');
    }
    emit(stage);
    // 普通优选在首个事件后保留稳定的取消窗口，避免并行 E2E 受机器调度速度影响。
    if (shouldHoldForCancellation && index === 0) {
      const deadline = Date.now() + 3_000;
      while (!cancelled && Date.now() < deadline) {
        await new Promise((resolve) => window.setTimeout(resolve, 25));
      }
    }
  }
  running = false;
  activeEvent = undefined;
  const report = { id: runId, started_at: iso(-2), finished_at: new Date().toISOString(), range_source: 'cloudflare-api', range_hash: mockRangeSnapshot.hash, results, ipv4_decision: { selected: results[0], changed: true, reason: 'score improved 18.4%' }, ipv6_decision: { selected: results[1], changed: false, reason: 'minimum hold kept current node' }, policy_applied: true } satisfies RunReport;
  latestBenchmark = { run_id: report.id, finished_at: report.finished_at, saved_at: report.finished_at, results: report.results };
  return report;
}

export function subscribeMockEvents(listener: (event: OptimizerEvent) => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export async function mockRequest<T>(method: string, parameters: Record<string, unknown> = {}): Promise<T> {
  await new Promise((resolve) => window.setTimeout(resolve, 40));
  switch (method) {
    case 'system.status': return baseStatus() as T;
    case 'optimizer.run': return runOptimization(true) as Promise<T>;
    case 'quickstart.plan': return {
      plan_id: `plan-${Date.now()}`,
      expires_at: new Date(Date.now() + 5 * 60_000).toISOString(),
      physical_path: baseStatus().physical_path,
      effects: ['system_routes', 'mihomo_policy'],
      warnings: [],
      detections: { 'generic-route': { present: true, manageable: true }, mihomo: proxyDetections.mihomo },
      can_apply: true,
      manual_required: false,
      auto_maintenance_enabled: false,
    } as QuickStartPlan as T;
    case 'quickstart.run': {
      if (new URLSearchParams(window.location.search).get('quickstart') === 'expired' && !window.sessionStorage.getItem('quickstart-expired-once')) {
        window.sessionStorage.setItem('quickstart-expired-once', 'true');
        throw new Error('plan_expired: quick-start plan expired; create a new plan');
      }
      const report = await runOptimization();
      return {
        report,
        mode: parameters.mode,
        status: 'verified',
        auto_maintenance_enabled: parameters.mode === 'apply_and_remember',
      } as QuickStartResult as T;
    }
    case 'optimizer.cancel': cancelled = running; return { cancelled } as T;
    case 'ranges.get': return mockRangeSnapshot as T;
    case 'ranges.update': return { snapshot: mockRangeSnapshot, updated: false } as T;
    case 'history.list': return history as T;
    case 'history.latest': return latestBenchmark as T;
    case 'routes.list': return routes as T;
    case 'proxy.detect': return proxyDetections as T;
    case 'acceleration.domains': return { observed: 0, verified: 1, activated: 0, discovered: discoveredDomains.length, policy_refreshed: false, domains: [manualDomain, ...discoveredDomains] } as T;
    case 'acceleration.discover': return { observed: 12, verified: 1, activated: 0, discovered: discoveredDomains.length, policy_refreshed: false, domains: [manualDomain, ...discoveredDomains] } as T;
    case 'acceleration.clear_discovered': {
      const cleared = discoveredDomains.length;
      discoveredDomains = [];
      return { cleared, accelerations_removed: cleared, policy_refreshed: cleared > 0 } as T;
    }
    case 'acceleration.domain_test':
      return {
        domain: String(parameters.domain), address: String(parameters.address), probe_url: 'https://ani.momoc.top/assets/test.bin',
        downloaded: 1_048_576, duration: '1s', download_mbps: 42.5, download_verified: true, tested_at: new Date().toISOString(),
      } as T;
    case 'acceleration.domain_apply':
      return {
        domain: String(parameters.domain), address: String(parameters.address), download_mbps: 42.5,
        download_verified: true, policy_refreshed: true, applied_at: new Date().toISOString(),
      } as T;
    case 'config.get': return mockConfig as T;
    case 'config.update': return { saved: true, hot_applied: true, policy_refreshed: false, restart_required: false } as T;
    case 'logs.tail': return [
      JSON.stringify({ time: iso(-3), level: 'INFO', component: 'optimizer', msg: '优选任务结束', run_id: 'run-20260801-0930', result: 'completed', duration: '2m41s' }),
      JSON.stringify({ time: iso(-4), level: 'INFO', component: 'proxy', msg: '代理策略验证完成', transaction_id: 'px-41c8b3', adapter: 'mihomo', result: 'verified' }),
      JSON.stringify({ time: iso(-5), level: 'WARN', component: 'ranges', msg: '备用数据源不可用，继续使用已验证缓存', result: 'fallback' }),
      JSON.stringify({ time: iso(-6), level: 'INFO', component: 'benchmark', msg: '候选连接质量测试完成', run_id: 'run-20260801-0930', candidates: 1000, qualified: 27 }),
    ] as T;
    case 'diagnostics.route': return {
      generated_at: iso(0), platform: 'windows', physical_path: baseStatus().physical_path,
      route: {
        target: String(parameters.target ?? ''),
        resolved: routes[0].verification,
        socket_source: '192.168.50.24', socket_connected: true,
        interface_matches: true, gateway_matches: true, source_matches: true, verified_direct: true,
      },
      virtual_interfaces: ['Mihomo Tun'], detected_proxy_processes: ['mihomo.exe'],
      proxy_environment_set: false, direct_policy_verified: true,
      warnings: ['已验证系统选路和 Socket 源地址；透明代理仍需结合代理 DIRECT 策略验证。'],
    } as DiagnosticReport as T;
    default: throw new Error(`mock method ${method} is not implemented`);
  }
}
