import { describe, expect, it } from 'vitest'
import { h } from 'vue'
import { mountWithI18n } from '../lib/testing/mountWithI18n'
import SyncResultPanel from './SyncResultPanel.vue'

function mountPanel(warnings: string[] = ['警告 1', '警告 2']) {
  return mountWithI18n({
    render: () =>
      h(SyncResultPanel, {
        title: '同期完了',
        result: {
          mode: 'incremental',
          fetched: 12,
          upserted: 8,
          deleted: 2,
          durationMs: 1250,
          warnings,
        },
      }),
  })
}

describe('SyncResultPanel', () => {
  it('同期件数・所要時間・警告をまとめて表示する', () => {
    const mounted = mountPanel()

    expect(mounted.host.querySelector('.result-title')?.textContent).toBe('同期完了')
    expect(mounted.host.textContent).toContain('取得: 12 件')
    expect(mounted.host.textContent).toContain('登録・更新: 8 件')
    expect(mounted.host.textContent).toContain('削除: 2 件')
    expect(mounted.host.textContent).toContain('所要時間: 1.3 秒')
    expect(
      Array.from(mounted.host.querySelectorAll('.warnings li')).map((item) => item.textContent),
    ).toEqual(['警告 1', '警告 2'])
    mounted.unmount()
  })

  it('警告がない場合は警告領域を表示しない', () => {
    const mounted = mountPanel([])

    expect(mounted.host.querySelector('.warnings')).toBeNull()
    mounted.unmount()
  })
})
