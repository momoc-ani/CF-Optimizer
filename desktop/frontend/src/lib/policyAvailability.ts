// shouldApplyPolicy 仅在运行时已激活策略适配器且用户明确请求时允许应用策略。
export function shouldApplyPolicy(policyAvailable: boolean, policyRequested: boolean): boolean {
  return policyAvailable && policyRequested;
}

// canApplyManualDomain 只有在策略适配器和当前已验证策略均可用时允许应用域名映射。
export function canApplyManualDomain(policyAvailable: boolean, policyVerified: boolean): boolean {
  return policyAvailable && policyVerified;
}
