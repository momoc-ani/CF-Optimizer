// shouldApplyPolicy 仅在运行时已激活策略适配器且用户明确请求时允许应用策略。
export function shouldApplyPolicy(policyAvailable: boolean, policyRequested: boolean): boolean {
  return policyAvailable && policyRequested;
}
