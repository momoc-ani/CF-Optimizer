/** redactLogLine 在导出前再次过滤常见认证字段。 */
export function redactLogLine(line: string): string {
  return line
    .replace(/(bearer\s+)[a-z0-9._~+/-]+/gi, '$1[REDACTED]')
    .replace(/("?(?:token|secret|password|authorization|cookie)"?\s*[:=]\s*)(?:"[^"]*"|[^,\s}]+)/gi, '$1"[REDACTED]"');
}
