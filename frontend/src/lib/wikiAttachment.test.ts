import { expect, it } from 'vitest'
import { formatAttachmentSize } from './wikiAttachment'
it('formats binary size boundaries without rounding into 1024 KB', () => {
 expect(formatAttachmentSize(0, 'en')).toBe('0 bytes')
 expect(formatAttachmentSize(1023, 'en')).toBe('1,023 bytes')
 expect(formatAttachmentSize(1536, 'en')).toBe('1.5 KB')
 expect(formatAttachmentSize(1048575, 'en')).toBe('1.0 MB')
 expect(formatAttachmentSize(-1, 'en')).toBeNull()
})
