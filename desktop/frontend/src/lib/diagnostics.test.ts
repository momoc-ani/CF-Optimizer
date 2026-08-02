import { describe, expect, it } from 'vitest';
import type { DiagnosticReport } from '../api/types';
import { routeDiagnosticPresentation } from './diagnostics';

const verifiedReport: DiagnosticReport = {
  generated_at: '2026-08-02T00:00:00Z',
  platform: 'windows',
  physical_path: {
    interface: '以太网 3',
    interface_index: 27,
    gateway_ipv4: '192.168.15.1',
    source_ipv4: ['192.168.15.116'],
  },
  route: {
    target: '104.16.132.229',
    resolved: {
      prefix: '0.0.0.0/0',
      gateway: '192.168.15.1',
      interface: '以太网 3',
      interface_index: 27,
      metric: 25,
      source_address: '192.168.15.116',
    },
    socket_source: '192.168.15.116',
    socket_connected: true,
    interface_matches: true,
    gateway_matches: true,
    source_matches: true,
    verified_direct: true,
  },
  virtual_interfaces: [],
  detected_proxy_processes: [],
  proxy_environment_set: false,
  direct_policy_verified: false,
  warnings: ['已验证系统选路和 Socket 源地址；透明代理仍需结合代理 DIRECT 策略验证。'],
};

describe('routeDiagnosticPresentation', () => {
  it('uses the backend route evidence instead of the removed legacy fields', () => {
    expect(routeDiagnosticPresentation(verifiedReport)).toEqual({
      verified: true,
      detail: '源地址 192.168.15.116；目标 104.16.132.229:443',
    });
  });

  it('lists mismatched evidence when the backend returns no connection error', () => {
    const report: DiagnosticReport = {
      ...verifiedReport,
      route: { ...verifiedReport.route, gateway_matches: false, verified_direct: false },
    };
    expect(routeDiagnosticPresentation(report)).toEqual({
      verified: false,
      detail: '证据不完整：物理网关不匹配',
    });
  });
});
