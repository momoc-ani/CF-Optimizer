/** joinConfigLines 将旧配置中的 null 集合安全投影为多行表单文本。 */
export function joinConfigLines(values: readonly string[] | null | undefined): string {
  return (values ?? []).join('\n');
}
