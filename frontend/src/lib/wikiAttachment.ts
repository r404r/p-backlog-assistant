export function formatAttachmentSize(bytes: number, locale: string): string | null {
 if (!Number.isSafeInteger(bytes) || bytes < 0) return null
 if (bytes < 1024) return `${bytes.toLocaleString(locale)} bytes`
 const mb = bytes >= 1048576 || Math.round(bytes / 1024 * 10) >= 10240
 return `${(bytes / (mb ? 1048576 : 1024)).toLocaleString(locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} ${mb ? 'MB' : 'KB'}`
}
