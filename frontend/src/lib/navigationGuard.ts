let guard: (() => Promise<boolean>) | undefined

export function registerNavigationGuard(check: () => Promise<boolean>): () => void {
  guard = check
  return () => { if (guard === check) guard = undefined }
}

export async function canLeaveCurrentView(): Promise<boolean> {
  return guard ? guard() : true
}
