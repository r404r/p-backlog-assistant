export function formatAttachmentSize(
  bytes: number,
  locale: string,
): string | null {
  if (!Number.isSafeInteger(bytes) || bytes < 0) return null
  if (bytes < 1024) return `${bytes.toLocaleString(locale)} bytes`
  const mb = bytes >= 1048576 || Math.round((bytes / 1024) * 10) >= 10240
  return `${(bytes / (mb ? 1048576 : 1024)).toLocaleString(locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} ${mb ? 'MB' : 'KB'}`
}

// 移動で消えた行にもキャンセルを表示する。保存先やファイル名は保持しない。
const cancelled = new Set<string>()
function cancellationKey(
  profile: string,
  project: number,
  wiki: number,
  attachment: number,
): string {
  return JSON.stringify([profile, project, wiki, attachment])
}
export function attachmentWasCancelled(
  profile: string,
  project: number,
  wiki: number,
  attachment: number,
): boolean {
  return (
    !!profile &&
    cancelled.has(cancellationKey(profile, project, wiki, attachment))
  )
}
export function rememberAttachmentCancellation(
  profile: string,
  project: number,
  wiki: number,
  attachment: number,
  value: boolean,
): void {
  if (!profile) return
  const key = cancellationKey(profile, project, wiki, attachment)
  if (value) cancelled.add(key)
  else cancelled.delete(key)
}
