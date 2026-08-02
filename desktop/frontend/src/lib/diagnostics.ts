import type { DiagnosticReport } from '../api/types';

export interface RouteDiagnosticPresentation {
  verified: boolean;
  detail: string;
}

/** routeDiagnosticPresentation 将服务端逐项证据转换为一致且不夸大的诊断结论。 */
export function routeDiagnosticPresentation(report: DiagnosticReport): RouteDiagnosticPresentation {
  const evidence = report.route;
  if (evidence.verified_direct) {
    return {
      verified: true,
      detail: `源地址 ${evidence.socket_source ?? '未知'}；目标 ${evidence.target}:443`,
    };
  }
  if (evidence.error) return { verified: false, detail: evidence.error };

  const missing: string[] = [];
  if (!evidence.socket_connected) missing.push('Socket 未连接');
  if (!evidence.interface_matches) missing.push('物理接口不匹配');
  if (!evidence.gateway_matches) missing.push('物理网关不匹配');
  if (!evidence.source_matches) missing.push('Socket 源地址不匹配');
  return {
    verified: false,
    detail: missing.length ? `证据不完整：${missing.join('、')}` : '后台未返回完整证据。',
  };
}
