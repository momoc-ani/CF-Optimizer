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
