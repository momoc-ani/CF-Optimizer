/** LogRecord 定义日志排序所需的可选时间字段。 */
export interface LogRecord {
  time?: string;
}

/** sortLogsNewestFirst 按可解析时间倒序排列日志，并稳定保留无效时间的原始顺序。 */
export function sortLogsNewestFirst<T extends LogRecord>(entries: T[]): T[] {
  return entries
    .map((entry, index) => ({ entry, index, timestamp: Date.parse(entry.time ?? '') }))
    .sort((left, right) => {
      const leftValid = Number.isFinite(left.timestamp);
      const rightValid = Number.isFinite(right.timestamp);
      if (!leftValid || !rightValid) {
        if (leftValid !== rightValid) return leftValid ? -1 : 1;
        return left.index - right.index;
      }
      if (left.timestamp === right.timestamp) return left.index - right.index;
      return right.timestamp - left.timestamp;
    })
    .map(({ entry }) => entry);
}
