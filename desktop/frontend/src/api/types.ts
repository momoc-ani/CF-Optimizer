export type VerificationState = 'verified' | 'pending' | 'failed' | 'rolled_back' | 'unknown';

export interface BuildMetadata {
  version: string;
  commit: string;
  date: string;
}

export interface Selection {
  ip: string;
  family: number;
  score: number;
  selected_at: string;
  last_successful_at: string;
  consecutive_failures: number;
  policy_verified: boolean;
}

export interface ResultSummary {
  ip: string;
  score: number;
  avg_latency: number;
  loss: number;
  mbps: number;
}

export interface RunSummary {
  id: string;
  started_at: string;
  finished_at: string;
  candidates: number;
  qualified: number;
  best?: ResultSummary[];
  selected_ipv4?: string;
  selected_ipv6?: string;
  switch_reason?: string;
  error?: string;
}

/** LatestBenchmark 描述后台最近一次成功持久化的候选明细。 */
export interface LatestBenchmark {
  run_id: string;
  finished_at: string;
  saved_at: string;
  results: BenchmarkResult[];
}

export interface ServiceState {
  version: number;
  updated_at: string;
  current_ipv4?: Selection;
  current_ipv6?: Selection;
  last_error?: string;
  last_started_at?: string;
  last_ended_at?: string;
  running: boolean;
}

export interface DomainDiscovery {
  domain: string;
  source: string;
  first_seen_at: string;
  last_seen_at: string;
  cloudflare_verified: boolean;
  preflight_verified: boolean;
  active: boolean;
  last_resolved_addresses?: string[];
  accelerated_addresses?: string[];
  verified_adapters?: string[];
  applied_at?: string;
  last_error?: string;
}

export interface DomainDiscoveryResult {
  observed: number;
  verified: number;
  activated: number;
  discovered: number;
  policy_refreshed: boolean;
  domains: DomainDiscovery[];
}

export interface DiscoveredDomainCleanupResult {
  cleared: number;
  accelerations_removed: number;
  policy_refreshed: boolean;
}

export interface Progress {
  stage: 'tcp' | 'tls' | 'download';
  completed: number;
  total: number;
  ip?: string;
  qualified: number;
  message?: string;
}

export interface OptimizerEvent {
  run_id: string;
  type: string;
  stage?: string;
  message?: string;
  progress?: Progress;
  timestamp: string;
}

export interface PhysicalPath {
  interface?: string;
  interface_index?: number;
  source_ipv4?: string[];
  source_ipv6?: string[];
  gateway_ipv4?: string;
  gateway_ipv6?: string;
}

export interface ScheduleStatus {
  enabled: boolean;
  interval: string;
  next_scheduled_at?: string;
  trigger?: 'startup' | 'interval' | 'network_change' | 'retry' | 'running' | 'disabled';
}

export interface SystemStatus {
  build: BuildMetadata;
  protocol_version: number;
  state: ServiceState;
  physical_path: PhysicalPath;
  policy_available: boolean;
  active_event?: OptimizerEvent;
  schedule: ScheduleStatus;
}

export interface BenchmarkResult {
  ip: string;
  family: number;
  attempts: number;
  successes: number;
  loss: number;
  avg_latency: number;
  p95_latency: number;
  jitter: number;
  tls_latency?: number;
  ttfb?: number;
  mbps?: number;
  tcp_qualified: boolean;
  tls_verified: boolean;
  download_verified: boolean;
  qualified: boolean;
  score: number;
  error?: string;
}

export interface Decision {
  current?: BenchmarkResult;
  selected?: BenchmarkResult;
  changed: boolean;
  reason: string;
}

export interface DomainAllocationResult {
  domain: string;
  source: string;
  resolved_addresses?: string[];
  assigned_address?: string;
  cloudflare_verified: boolean;
  preflight_verified: boolean;
  error?: string;
}

export interface BenchmarkPathEvidence {
  adapter: string;
  interface?: string;
  target: string;
  guard_applied: boolean;
  socket_bound: boolean;
  proxy_observed: boolean;
  direct_verified: boolean;
  physical_route_used: boolean;
  rule?: string;
  rule_payload?: string;
  verification: string;
}

export interface RunReport {
  id: string;
  started_at: string;
  finished_at: string;
  range_source: string;
  range_hash: string;
  results: BenchmarkResult[];
  ipv4_decision: Decision;
  ipv6_decision: Decision;
  policy_applied: boolean;
  benchmark_path?: BenchmarkPathEvidence[];
  domain_allocations?: DomainAllocationResult[];
  warnings?: string[];
}

export type QuickStartMode = 'apply_once' | 'apply_and_remember';
export type QuickStartStatus = 'verified' | 'partial' | 'rolled_back';

export interface QuickStartPlan {
  plan_id: string;
  expires_at: string;
  physical_path: PhysicalPath;
  effects: string[];
  warnings?: string[];
  detections: ProxyDetections;
  can_apply: boolean;
  manual_required: boolean;
  auto_maintenance_enabled: boolean;
}

export interface QuickStartResult {
  report: RunReport;
  mode: QuickStartMode;
  status: QuickStartStatus;
  auto_maintenance_enabled: boolean;
  persistence_warning?: string;
  error?: string;
}

export interface RangeSnapshot {
  version: number;
  fetched_at: string;
  source: string;
  etag?: string;
  hash: string;
  ipv4: string[];
  ipv6: string[];
  include?: string[];
  exclude?: string[];
}

export interface RouteSpec {
  prefix: string;
  gateway: string;
  interface: string;
  interface_index?: number;
  metric: number;
}

export interface ResolvedRoute extends RouteSpec {
  source_address?: string;
}

export interface RouteTransaction {
  id: string;
  operation: string;
  route: RouteSpec;
  temporary: boolean;
  state: string;
  started_at: string;
  updated_at: string;
  verification?: RouteSpec & { source_address?: string };
  error?: string;
}

export interface ProxyDetection {
  present: boolean;
  manageable: boolean;
  version?: string;
  endpoint?: string;
  config_path?: string;
  message?: string;
}

export type ProxyDetections = Record<string, ProxyDetection>;

export interface AppConfig {
  version: number;
  data_dir: string;
  schedule: {
    enabled: boolean;
    interval: string;
    run_on_network_change: boolean;
    network_poll: string;
  };
  ranges: {
    source: string;
    api_url: string;
    ipv4_url: string;
    ipv6_url: string;
    refresh_interval: string;
    stale_after: string;
    max_change_percent: number;
    request_timeout: string;
    include: string[] | null;
    exclude: string[] | null;
  };
  benchmark: {
    ipv4: boolean;
    ipv6: boolean;
    candidates: number;
    connect_attempts: number;
    concurrency: number;
    connect_timeout: string;
    latency_limit: string;
    loss_limit: number;
    download_top: number;
    download_url: string;
    tls_server_name: string;
    tls_timeout: string;
    download_duration: string;
    download_max_bytes: number;
    switch_improvement: number;
    minimum_hold: string;
    failure_threshold: number;
    failure_cooldown: string;
    daily_seed: string;
  };
  network: {
    interface: string;
    gateway_ipv4: string;
    gateway_ipv6: string;
    manage_routes: boolean;
    command_timeout: string;
  };
  acceleration: {
    enabled: boolean;
    manual_domains: string[] | null;
    excluded_domains: string[] | null;
    auto_discover: boolean;
    auto_apply: boolean;
    discovery_interval: string;
    max_discovered_domains: number;
    apply_verification_timeout: string;
    apply_attempt_timeout: string;
    apply_retry_interval: string;
    apply_max_attempts: number;
  };
  proxy: Record<string, unknown>;
  hosts: { enabled: boolean; path: string; domains: string[] };
  ipc: { endpoint: string };
  history: { summary_retention: string; detail_retention: string; max_runs: number };
}

export interface DiagnosticReport {
  generated_at: string;
  platform: string;
  physical_path: PhysicalPath;
  route: {
    target: string;
    resolved: ResolvedRoute;
    socket_source?: string;
    socket_connected: boolean;
    interface_matches: boolean;
    gateway_matches: boolean;
    source_matches: boolean;
    verified_direct: boolean;
    error?: string;
  };
  virtual_interfaces: string[] | null;
  detected_proxy_processes: string[] | null;
  proxy_environment_set: boolean;
  direct_policy_verified: boolean;
  warnings: string[];
}
