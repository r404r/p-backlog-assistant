// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import MarkdownBody from './MarkdownBody.vue'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import type { WikiPage, WikiImageResult } from '../lib/backend'
const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  cancel: vi.fn(async () => {}),
  copy: vi.fn(async () => {}),
  open: vi.fn(),
}))
vi.mock('../lib/backend', async (original) => ({
  ...(await original<typeof import('../lib/backend')>()),
  getBackend: () => ({ getWikiAttachmentImage: mocks.get, cancelWikiImageLoads: mocks.cancel }),
  copyToClipboard: mocks.copy,
  openExternalURL: mocks.open,
}))
const page: WikiPage = {
  id: 1,
  projectId: 101,
  name: 'Guide',
  content: '#image(7)',
  created: '',
  updated: 'T',
  contentUpdated: 'T',
  contentFetchedAt: 'T',
  tags: [],
  createdUser: '',
  updatedUser: '',
  textFormattingRule: 'markdown',
  attachments: [{ id: 7, wikiId: 1, name: 'test.png', size: 20 }],
}
const ready: WikiImageResult = {
  code: '',
  mime: 'image/png',
  dataBase64:
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aY1sAAAAASUVORK5CYII=',
  width: 1,
  height: 1,
}
let mounted: MountedApp | undefined
const create = vi.fn(() => `blob:test-${Math.random()}`),
  revoke = vi.fn()
const observers: FakeObserver[] = []
class FakeObserver {
  nodes = new Set<Element>()
  callback: IntersectionObserverCallback
  constructor(callback: IntersectionObserverCallback, readonly options?: IntersectionObserverInit) {
    this.callback = callback
    observers.push(this)
  }
  observe(node: Element) {
    this.nodes.add(node)
  }
  unobserve(node: Element) {
    this.nodes.delete(node)
  }
  disconnect = vi.fn(() => {
    this.nodes.clear()
  })
  reveal() {
    this.callback(
      Array.from(this.nodes, (node) => ({
        target: node,
        isIntersecting: true,
      })) as IntersectionObserverEntry[],
      this as unknown as IntersectionObserver,
    )
  }
}
async function flush() {
  for (let i = 0; i < 12; i++) {
    await Promise.resolve()
    await nextTick()
  }
  await new Promise((resolve) => setTimeout(resolve, 25))
  await nextTick()
}
function reveal() {
  for (const observer of observers) observer.reveal()
}
beforeEach(() => {
  mocks.get.mockReset()
  mocks.get.mockResolvedValue(ready)
  mocks.cancel.mockClear()
  mocks.copy.mockClear()
  mocks.open.mockClear()
  create.mockClear()
  revoke.mockClear()
  observers.length = 0
  vi.stubGlobal('IntersectionObserver', FakeObserver)
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) =>
    setTimeout(() => callback(performance.now()), 0),
  )
  vi.stubGlobal('cancelAnimationFrame', (id: number) => clearTimeout(id))
  class TestURL extends URL {
    static createObjectURL = create
    static revokeObjectURL = revoke
  }
  vi.stubGlobal('URL', TestURL)
})
afterEach(() => {
  mounted?.unmount()
  mounted = undefined
  vi.unstubAllGlobals()
})
describe('MarkdownBody の操作と寿命', () => {
  it('viewportに入るまで取得せず、同じ添付参照を共有してunmount時に一度だけrevokeする', async () => {
    mounted = mountWithI18n(
      defineComponent(
        () => () =>
          h(MarkdownBody, {
            source: '#image(7) #image(7)',
            wiki: { page, pages: [page] },
            profile: 'p',
          }),
      ),
    )
    await flush()
    expect(mocks.get).not.toHaveBeenCalled()
    reveal()
    await flush()
    expect(mocks.get).toHaveBeenCalledTimes(1)
    expect(create).toHaveBeenCalledTimes(1)
    expect(mounted.host.querySelectorAll('img')).toHaveLength(2)
    mounted.unmount()
    mounted = undefined
    expect(revoke).toHaveBeenCalledTimes(1)
    expect(observers.every((o) => o.nodes.size === 0)).toBe(true)
    expect(mocks.cancel).toHaveBeenCalledWith('p', expect.stringMatching(/^img-/))
  })
  it('遅延応答後にページやprofileが変わっても古いblobを作らない', async () => {
    let resolve!: (value: WikiImageResult) => void
    mocks.get.mockImplementation(
      () =>
        new Promise<WikiImageResult>((r) => {
          resolve = r
        }),
    )
    const current = ref({ source: '#image(7)', profile: 'old', wiki: { page, pages: [page] } })
    mounted = mountWithI18n(defineComponent(() => () => h(MarkdownBody, current.value)))
    await flush()
    reveal()
    await flush()
    current.value = {
      source: '# next',
      profile: 'new',
      wiki: { page: { ...page, id: 2, attachments: [] }, pages: [page] },
    }
    await flush()
    resolve(ready)
    await flush()
    expect(create).not.toHaveBeenCalled()
    expect(mounted.host.querySelector('img')).toBeNull()
    expect(mocks.cancel).toHaveBeenCalledWith('old', expect.any(String))
  })
  it('エラーを文字表示し、手動再試行を新世代で取得する', async () => {
    mocks.get.mockResolvedValueOnce({ ...ready, code: 'forbidden', dataBase64: '' })
    mounted = mountWithI18n(
      defineComponent(
        () => () =>
          h(MarkdownBody, { source: '#image(7)', profile: 'p', wiki: { page, pages: [page] } }),
      ),
    )
    await flush()
    reveal()
    await flush()
    expect(mounted.host.textContent).toContain('権限がありません')
    const first = mocks.get.mock.calls[0][1].requestId
    mounted.host.querySelector<HTMLButtonElement>('.markdown-image-actions button')!.click()
    await flush()
    reveal()
    await flush()
    expect(mocks.get.mock.calls[1][1].requestId).not.toBe(first)
    expect(mounted.host.querySelector('img')).not.toBeNull()
  })
  it('原文をコピーして成功/失敗を通知し、Enterで内部リンクを通知する', async () => {
    const navigate = vi.fn()
    mounted = mountWithI18n(
      defineComponent(
        () => () =>
          h(MarkdownBody, {
            source: '```js\nconst x = "<img>"\n```\n\n[[Guide]]',
            wiki: { page, pages: [page] },
            profile: 'p',
            onNavigate: navigate,
          }),
      ),
    )
    await flush()
    mounted.host.querySelector<HTMLButtonElement>('button[data-md-code]')!.click()
    await flush()
    expect(mocks.copy).toHaveBeenCalledWith('const x = "<img>"\n')
    expect(mounted.host.textContent).toContain('コードをコピーしました')
    mocks.copy.mockRejectedValueOnce(new Error('failure'))
    mounted.host.querySelector<HTMLButtonElement>('button[data-md-code]')!.click()
    await flush()
    expect(mounted.host.textContent).toContain('コピーできませんでした')
    const anchor = mounted.host.querySelector('a')!
    anchor.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    await flush()
    expect(navigate).toHaveBeenCalledWith(1)
    anchor.dispatchEvent(
      new MouseEvent('click', { button: 0, ctrlKey: true, bubbles: true, cancelable: true }),
    )
    expect(navigate).toHaveBeenCalledTimes(1)
  })
  it('大きなページは原文を欠落させず画像と高亮を停止する', async () => {
    const source = '#image(7)\n' + 'a'.repeat(2 * 1024 * 1024)
    mounted = mountWithI18n(
      defineComponent(
        () => () => h(MarkdownBody, { source, profile: 'p', wiki: { page, pages: [page] } }),
      ),
    )
    await flush()
    reveal()
    await flush()
    expect(mounted.host.querySelector('pre')?.textContent).toBe(source)
    expect(mocks.get).not.toHaveBeenCalled()
  })
  it('同期中と偽MIMEでは画像を表示しない', async () => {
    const busy = ref(true),
      wiki = { page, pages: [page] }
    mocks.get.mockResolvedValue({ ...ready, mime: 'image/svg+xml' })
    mounted = mountWithI18n(
      defineComponent(
        () => () =>
          h(MarkdownBody, { source: '#image(7)', profile: 'p', wiki, disabled: busy.value }),
      ),
    )
    await flush()
    reveal()
    await flush()
    expect(mocks.get).not.toHaveBeenCalled()
    busy.value = false
    await flush()
    expect(create).not.toHaveBeenCalled()
    expect(mounted.host.querySelector('img')).toBeNull()
    expect(mounted.host.textContent).toContain('画像を確認できません')
  })
})

it.each(['wiki', 'comment'] as const)('rebinds observers, heading navigation, position and delayed images to the new container (%s)', async (variant) => {
  const first = document.createElement('div'), second = document.createElement('div')
  const container = ref<HTMLElement>(first)
  const component = ref<InstanceType<typeof MarkdownBody>>()
  const active = vi.fn()
  const wiki = { page, pages: [page] }
  let resolveImage!: (result: WikiImageResult) => void
  mocks.get.mockImplementation(() => new Promise<WikiImageResult>(resolve => { resolveImage = resolve }))
  // eslint-disable-next-line vue/one-component-per-file -- スクロール所有者の差替えを検証する専用ホスト。
  mounted = mountWithI18n(defineComponent({ setup: () => () => h(MarkdownBody, { ref: component, source: '# One\n\n## Two\n\n#image(7)', variant, scrollContainer: container.value, profile: 'p', wiki, onActive: active }) }))
  await flush()
  const heading = mounted.host.querySelector<HTMLElement>('h2')!
  const old = observers.at(-1)!
  expect(old.options?.root).toBe(first)
  expect(old.nodes.size).toBe(1)
  const remove = vi.spyOn(first, 'removeEventListener'), add = vi.spyOn(second, 'addEventListener')
  vi.spyOn(first, 'getBoundingClientRect').mockReturnValue({ top: 0 } as DOMRect)
  vi.spyOn(second, 'getBoundingClientRect').mockReturnValue({ top: 100 } as DOMRect)
  const rect = vi.spyOn(heading, 'getBoundingClientRect').mockReturnValue({ top: 120 } as DOMRect)
  container.value = second
  await flush()
  expect(mounted.host.querySelector('h2')).toBe(heading)
  expect(remove).toHaveBeenCalledWith('scroll', expect.any(Function))
  expect(add).toHaveBeenCalledWith('scroll', expect.any(Function), { passive: true })
  expect(old.disconnect).toHaveBeenCalledTimes(1)
  expect(old.nodes.size).toBe(0)
  const current = observers.at(-1)!
  expect(current).not.toBe(old)
  expect(current.options?.root).toBe(second)
  expect(current.nodes.size).toBe(1)
  expect(active).toHaveBeenLastCalledWith(heading.id)
  // 目次からの移動は新しい所有者だけを動かす。
  first.scrollTop = 17; second.scrollTop = 80
  rect.mockReturnValue({ top: 320 } as DOMRect)
  component.value!.goToHeading(heading.id)
  expect(first.scrollTop).toBe(17)
  expect(second.scrollTop).toBe(284)
  expect(active).toHaveBeenLastCalledWith(heading.id)
  // scrollイベントでも新rootの上端を基準に現在見出しを判定する。
  active.mockClear()
  rect.mockReturnValue({ top: 120 } as DOMRect)
  second.dispatchEvent(new Event('scroll')); await flush()
  expect(active).toHaveBeenLastCalledWith(heading.id)
  active.mockClear()
  first.dispatchEvent(new Event('scroll')); await flush()
  expect(active).not.toHaveBeenCalled()
  expect(mocks.get).not.toHaveBeenCalled()
  old.reveal(); await flush()
  expect(mocks.get).not.toHaveBeenCalled()
  current.reveal(); await flush()
  expect(mocks.get).toHaveBeenCalledTimes(1)
  const request = mocks.get.mock.calls[0]![1].requestId
  resolveImage(ready); await flush()
  expect(mounted.host.querySelector('img')?.getAttribute('src')).toMatch(/^blob:test-/)
  expect(mocks.get.mock.calls[0]![1].requestId).toBe(request)
  expect(mocks.cancel).not.toHaveBeenCalled()
})
it.each([undefined, 'comment'] as const)('keeps ancestor scroll detection when no container is supplied (%s)', async (variant) => {
  const host = document.createElement('div')
  host.style.overflowY = 'auto'
  Object.defineProperty(host, 'scrollHeight', { value: 900 })
  Object.defineProperty(host, 'clientHeight', { value: 100 })
  const add = vi.spyOn(host, 'addEventListener')
  // eslint-disable-next-line vue/one-component-per-file -- 既定の祖先検出を検証する専用ホスト。
  const Parent = defineComponent({ setup: () => () => h('div', { ref: (el: unknown) => { if (el instanceof HTMLElement) { el.style.overflowY = 'auto'; Object.defineProperty(el, 'scrollHeight', { value: 900, configurable: true }); Object.defineProperty(el, 'clientHeight', { value: 100, configurable: true }); el.addEventListener = host.addEventListener.bind(host) } } }, h(MarkdownBody, { source: '# One', variant })) })
  mounted = mountWithI18n(Parent)
  await nextTick(); await nextTick(); await nextTick()
  expect(add).toHaveBeenCalledWith('scroll', expect.any(Function), { passive: true })
})
