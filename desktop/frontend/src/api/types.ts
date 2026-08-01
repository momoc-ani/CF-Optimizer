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

export interface ServiceState {
  version: number;
  updated_at: string;
  current_ipv4?: Selection;
  current_ipv6?: Selection;
  history: RunSummary[];
  last_error?: string;
  last_started_at?: string;
  last_ended_at?: string;
  running: boolean;
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

export interface SystemStatus {
  build: BuildMetadata;
  protocol_version: number;
  state: ServiceState;
  physical_path: PhysicalPath;
  active_event?: OptimizerEvent;
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
  version?: string;
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
    include: string[];
    exclude: string[];
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
  proxy: Record<string, unknown>;
  hosts: { enabled: boolean; path: string; domains: string[] };
  ipc: { endpoint: string };
  history: { summary_retention: string; detail_retention: string; max_runs: number };
}

export interface DiagnosticReport {
  target: string;
  physical_path: PhysicalPath;
  expected_route?: RouteSpec;
  observed_route?: RouteSpec & { source_address?: string };
  direct_connection?: { local_address?: string; remote_address?: string; error?: string };
  virtual_interfaces?: string[];
  proxy_processes?: string[];
  warnings?: string[];
  verified: boolean;
}
