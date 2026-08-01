/** verificationTone 只有在存在明确验证结果时返回成功状态。 */
export function verificationTone(verified?: boolean): 'verified' | 'neutral' {
  return verified ? 'verified' : 'neutral';
}
