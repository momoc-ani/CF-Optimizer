// shouldApplyPolicy 仅在运行时已激活策略适配器且用户明确请求时允许应用策略。
export function shouldApplyPolicy(policyAvailable: boolean, policyRequested: boolean): boolean {
  return policyAvailable && policyRequested;
}

// canApplyManualDomain 只接受与当前草稿一致且已达标的最近一次手动测速证据。
export function canApplyManualDomain(downloadVerified: boolean, testedAddress: string | undefined, draftAddress: string): boolean {
  return downloadVerified && Boolean(testedAddress) && testedAddress === draftAddress.trim();
}
