/** joinConfigLines 将旧配置中的 null 集合安全投影为多行表单文本。 */
export function joinConfigLines(values: readonly string[] | null | undefined): string {
  return (values ?? []).join('\n');
}

/** parseDomainLines 将多行或逗号分隔的精确域名规范化为稳定去重列表。 */
export function parseDomainLines(value: string): string[] {
  return [...new Set(value
    .split(/[\n,]/)
    .map((item) => item.trim().toLowerCase().replace(/\.$/, ''))
    .filter(Boolean))]
    .sort();
}

/** retainConfiguredManualMappings 只保留仍属于手动域名集合的映射，避免删除域名时提交孤立配置。 */
export function retainConfiguredManualMappings(
  mappings: Readonly<Record<string, string>> | null | undefined,
  manualDomains: readonly string[],
): Record<string, string> {
  const configured = new Set(manualDomains.map((domain) => domain.trim().toLowerCase().replace(/\.$/, '')).filter(Boolean));
  return Object.fromEntries(Object.entries(mappings ?? {}).filter(([domain]) => {
    return configured.has(domain.trim().toLowerCase().replace(/\.$/, ''));
  }));
}
