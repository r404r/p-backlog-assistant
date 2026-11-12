<script setup lang="ts">
// WebView の Worker / clipboard / 画像decode実体は実機で別途確認する。
import { nextTick, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { copyToClipboard, getBackend, openExternalURL } from '../lib/backend'
import { markdownLinkHref } from '../lib/markdown'
import {
  prepareMarkdownDocument,
  type Heading,
  type MarkdownDocument,
  type ReadyImage,
  type WikiContext,
} from '../lib/markdownDocument'
const imageErrorKeys: Record<string, string> = {
  not_found: 'wiki.render.imageErrors.not_found',
  not_own_attachment: 'wiki.render.imageErrors.not_own_attachment',
  stale: 'wiki.render.imageErrors.stale',
  unauthorized: 'wiki.render.imageErrors.unauthorized',
  forbidden: 'wiki.render.imageErrors.forbidden',
  unsupported_type: 'wiki.render.imageErrors.unsupported_type',
  too_large: 'wiki.render.imageErrors.too_large',
  invalid_image: 'wiki.render.imageErrors.invalid_image',
  timeout: 'wiki.render.imageErrors.timeout',
  cancelled: 'wiki.render.imageErrors.cancelled',
  network: 'wiki.render.imageErrors.network',
  busy: 'wiki.render.imageErrors.busy',
}
const props = defineProps<{
  source: string
  wiki?: WikiContext
  profile?: string
  disabled?: boolean
  variant?: 'wiki' | 'comment'
  scrollContainer?: HTMLElement | null
  allowLarge?: boolean
}>()
const emit = defineEmits<{ headings: [Heading[]]; active: [string]; navigate: [number] }>()
const { t, locale } = useI18n()
const root = ref<HTMLElement | null>(null),
  html = ref<string[]>([]),
  status = ref('')
const doc = shallowRef<MarkdownDocument>()
const images = new Map<number, ReadyImage>(),
  errors = new Map<number, string>(),
  highlights = new Map<number, string>()
const pending = ref<number[]>([]),
  failed = ref<number[]>([])
const large = ref(false),
  retryVersion = ref(0)
let generation = 0,
  requestId = '',
  currentProfile = '',
  disposed = false
let observer: IntersectionObserver | undefined, positionObserver: ResizeObserver | undefined
let worker: Worker | undefined,
  timer: ReturnType<typeof setTimeout> | undefined,
  frame = 0,
  highlightBytes = 0
let activeJob = -1,
  workerReady = false
const attemptedCodes = new Set<number>()
const queued = new Set<number>(),
  loading = new Set<number>(),
  imageQueue: number[] = [],
  seenImages = new Set<number>()
let scrollRoot: HTMLElement | null = null,
  scrollFrame = 0
function observeVisible() {
  if (!root.value || !observer) return
  for (const node of root.value.querySelectorAll('[data-md-image],button[data-md-code]'))
    observer.observe(node)
}
function refresh() {
  if (!doc.value || disposed) return
  const gen = generation
  const focused = root.value?.contains(document.activeElement)
    ? (document.activeElement as HTMLElement)
    : null
  const headingFocus = focused?.id,
    codeFocus = focused?.getAttribute('data-md-code')
  html.value = doc.value.renderParts({ images, errors, highlights })
  void nextTick(() => {
    if (gen === generation) {
      observeVisible()
      onScroll()
      if (headingFocus)
        Array.from(root.value?.querySelectorAll<HTMLElement>('[id]') ?? [])
          .find((n) => n.id === headingFocus)
          ?.focus({ preventScroll: true })
      else if (codeFocus !== null && codeFocus !== undefined)
        Array.from(root.value?.querySelectorAll<HTMLElement>('button[data-md-code]') ?? [])
          .find((n) => n.getAttribute('data-md-code') === codeFocus)
          ?.focus({ preventScroll: true })
    }
  })
}
function schedule() {
  if (!frame)
    frame = requestAnimationFrame(() => {
      frame = 0
      refresh()
    })
}
function stopWorker() {
  if (timer) clearTimeout(timer)
  timer = undefined
  worker?.terminate()
  worker = undefined
  workerReady = false
  activeJob = -1
}
function cleanup() {
  generation++
  observer?.disconnect()
  positionObserver?.disconnect()
  observer = undefined
  positionObserver = undefined
  scrollRoot?.removeEventListener('scroll', onScroll)
  window.removeEventListener('resize', onScroll)
  if (scrollFrame) cancelAnimationFrame(scrollFrame)
  scrollFrame = 0
  stopWorker()
  attemptedCodes.clear()
  queued.clear()
  imageQueue.length = 0
  seenImages.clear()
  loading.clear()
  pending.value = []
  failed.value = []
  if (frame) cancelAnimationFrame(frame)
  frame = 0
  if (requestId && currentProfile)
    void getBackend()
      .cancelWikiImageLoads(currentProfile, requestId)
      .catch(() => {})
  html.value = []
  doc.value = undefined
  for (const url of new Set([...images.values()].map((img) => img.url))) URL.revokeObjectURL(url)
  images.clear()
  errors.clear()
  highlights.clear()
  highlightBytes = 0
}
function getScrollRoot() {
  if (props.scrollContainer) return props.scrollContainer
  let parent = root.value?.parentElement ?? null
  while (parent) {
    if (
      /(auto|scroll)/.test(getComputedStyle(parent).overflowY) &&
      parent.scrollHeight > parent.clientHeight
    )
      return parent
    parent = parent.parentElement
  }
  return document.scrollingElement as HTMLElement
}
function updatePosition() {
  const el = root.value
  if (!el || !doc.value) return
  const top =
    scrollRoot && scrollRoot !== document.scrollingElement
      ? scrollRoot.getBoundingClientRect().top
      : 0
  let active = doc.value.headings[0]?.id ?? ''
  for (const h of el.querySelectorAll<HTMLElement>('h1[id],h2[id],h3[id],h4[id],h5[id],h6[id]')) {
    if (h.getBoundingClientRect().top <= top + 40) active = h.id
    else break
  }
  emit('active', active)
}
function onScroll() {
  if (!scrollFrame)
    scrollFrame = requestAnimationFrame(() => {
      scrollFrame = 0
      updatePosition()
    })
}
function goToHeading(id: string) {
  const heading = [...(root.value?.querySelectorAll<HTMLElement>('[id]') ?? [])].find(
    (el) => el.id === id,
  )
  if (!heading || !doc.value?.headings.some((h) => h.id === id)) return
  const scroller = scrollRoot ?? getScrollRoot()
  if (scroller === document.scrollingElement)
    window.scrollBy({ top: heading.getBoundingClientRect().top - 16 })
  else
    scroller.scrollTop +=
      heading.getBoundingClientRect().top - scroller.getBoundingClientRect().top - 16
  heading.focus({ preventScroll: true })
  emit('active', id)
}
defineExpose({ goToHeading })
function runHighlight() {
  if (!workerReady || !worker || activeJob >= 0 || !doc.value || !queued.size) return
  const index = queued.values().next().value as number
  queued.delete(index)
  activeJob = index
  const block = doc.value.codeBlocks[index]
  timer = setTimeout(() => {
    stopWorker()
    queued.clear()
  }, 100)
  worker.postMessage({ index, ...block })
}
function highlight(index: number) {
  const block = doc.value?.codeBlocks[index]
  if (
    !block ||
    highlights.has(index) ||
    queued.has(index) ||
    index === activeJob ||
    block.language === 'text' ||
    large.value ||
    attemptedCodes.has(index)
  )
    return
  const bytes = new TextEncoder().encode(block.source).length
  if (bytes > 20 * 1024 || highlightBytes + bytes > 200 * 1024 || typeof Worker === 'undefined')
    return
  highlightBytes += bytes
  attemptedCodes.add(index)
  queued.add(index)
  if (!worker) {
    const gen = generation
    try {
      worker = new Worker(new URL('../lib/markdownHighlight.worker.ts', import.meta.url), {
        type: 'module',
      })
      worker.onmessage = (event) => {
        if (gen !== generation) return
        if (event.data.ready) {
          workerReady = true
          runHighlight()
          return
        }
        if (event.data.index !== activeJob) return
        if (timer) clearTimeout(timer)
        timer = undefined
        if (typeof event.data.html === 'string' && event.data.html)
          highlights.set(activeJob, event.data.html)
        activeJob = -1
        schedule()
        runHighlight()
      }
      worker.onerror = () => {
        stopWorker()
        queued.clear()
      }
    } catch {
      stopWorker()
      queued.clear()
    }
  }
  runHighlight()
}
function imageLabel(code: string) {
  return t(imageErrorKeys[Object.hasOwn(imageErrorKeys, code) ? code : 'network'])
}
function imageError(index: number, code: string) {
  errors.set(index, imageLabel(code))
  failed.value = [...new Set([...failed.value, index])]
  schedule()
}
async function pumpImages() {
  if (props.disabled || large.value || !props.wiki || !props.profile || !doc.value) return
  while (loading.size < 2 && imageQueue.length) {
    const index = imageQueue.shift()!,
      slot = doc.value.imageSlots[index]
    if (!slot || slot.code || loading.has(index) || images.has(index)) continue
    const gen = generation,
      currentDoc = doc.value,
      profile = props.profile
    loading.add(index)
    pending.value = [...loading]
    void (async () => {
      try {
        const page = props.wiki!.page
        const result = await getBackend().getWikiAttachmentImage(profile, {
          projectId: page.projectId,
          wikiId: page.id,
          attachmentId: slot.attachmentId,
          expectedUpdated: page.contentUpdated,
          requestId,
        })
        if (gen !== generation || currentDoc !== doc.value) return
        if (result.code) {
          imageError(index, result.code)
          return
        }
        if (
          !['image/png', 'image/jpeg'].includes(result.mime) ||
          !Number.isInteger(result.width) ||
          !Number.isInteger(result.height) ||
          result.width <= 0 ||
          result.height <= 0 ||
          result.width > 8192 ||
          result.height > 8192 ||
          result.width * result.height > 16000000 ||
          result.dataBase64.length > Math.ceil((8 * 1024 * 1024) / 3) * 4
        ) {
          imageError(index, 'invalid_image')
          return
        }
        const bytes = Uint8Array.from(atob(result.dataBase64), (c) => c.charCodeAt(0))
        let totalBytes = bytes.length,
          totalPixels = result.width * result.height
        for (const ready of new Set(images.values())) totalPixels += ready.width * ready.height
        for (const ready of imageSizes.values()) totalBytes += ready
        if (totalBytes > 32 * 1024 * 1024 || totalPixels > 16000000) {
          const top =
            scrollRoot && scrollRoot !== document.scrollingElement
              ? scrollRoot.getBoundingClientRect().top
              : 0
          const bottom =
            scrollRoot && scrollRoot !== document.scrollingElement
              ? scrollRoot.getBoundingClientRect().bottom
              : window.innerHeight
          for (const cached of new Set(images.values())) {
            const indices = [...images].filter(([, value]) => value === cached).map(([i]) => i)
            const visible = indices.some((i) =>
              Array.from(root.value?.querySelectorAll<HTMLElement>('[data-md-image]') ?? []).some(
                (node) =>
                  node.getAttribute('data-md-image') === String(i) &&
                  node.getBoundingClientRect().bottom > top - 200 &&
                  node.getBoundingClientRect().top < bottom + 200,
              ),
            )
            if (visible) continue
            totalPixels -= cached.width * cached.height
            for (const i of indices) {
              images.delete(i)
              seenImages.delete(i)
              totalBytes -= imageSizes.get(i) ?? 0
              imageSizes.delete(i)
            }
            URL.revokeObjectURL(cached.url)
            if (totalBytes <= 32 * 1024 * 1024 && totalPixels <= 16000000) break
          }
        }
        if (
          bytes.length > 8 * 1024 * 1024 ||
          totalBytes > 32 * 1024 * 1024 ||
          totalPixels > 16000000
        ) {
          imageError(index, 'too_large')
          return
        }
        if (gen !== generation) return
        const ready = currentDoc.registerImage(index, {
          url: URL.createObjectURL(new Blob([bytes], { type: result.mime })),
          width: result.width,
          height: result.height,
        })
        for (const [i, other] of currentDoc.imageSlots.entries())
          if (other.attachmentId === slot.attachmentId) images.set(i, ready)
        imageSizes.set(index, bytes.length)
        errors.delete(index)
        failed.value = failed.value.filter((i) => i !== index)
        schedule()
      } catch {
        if (gen === generation) imageError(index, 'network')
      } finally {
        if (gen === generation) {
          loading.delete(index)
          pending.value = [...loading]
          void pumpImages()
        }
      }
    })()
  }
}
const imageSizes = new Map<number, number>()
function loadImage(index: number) {
  const slot = doc.value?.imageSlots[index]
  if (!slot || slot.code) return
  index = doc.value!.imageSlots.findIndex((s) => s.attachmentId === slot.attachmentId)
  if (seenImages.has(index)) return
  seenImages.add(index)
  imageQueue.push(index)
  void pumpImages()
}
function retry(_index: number) {
  retryVersion.value++
}
function cancelImages() {
  if (props.profile)
    void getBackend()
      .cancelWikiImageLoads(props.profile, requestId)
      .catch(() => {})
  imageQueue.length = 0
}
function onImageError(event: Event) {
  const target = event.target
  if (!(target instanceof HTMLImageElement)) return
  const index = Number(target.parentElement?.getAttribute('data-md-image'))
  const ready = images.get(index)
  if (ready) {
    for (const [i, img] of images) if (img === ready) images.delete(i)
    imageSizes.delete(index)
    URL.revokeObjectURL(ready.url)
    imageError(index, 'invalid_image')
  }
}
async function action(event: MouseEvent | KeyboardEvent) {
  if (event instanceof KeyboardEvent && event.key !== 'Enter') return
  if (
    event instanceof MouseEvent &&
    (event.button !== 0 || event.ctrlKey || event.metaKey || event.altKey || event.shiftKey)
  ) {
    event.preventDefault()
    return
  }
  if (!(event.target instanceof Element) || !root.value?.contains(event.target) || !doc.value)
    return
  const button = event.target.closest('button[data-md-action=copy]')
  if (button) {
    // button は native Enter/Space による click を用いる。keydown の二重実行を避ける。
    if (event instanceof KeyboardEvent) return
    const block = doc.value.codeBlocks[Number(button.getAttribute('data-md-code'))],
      gen = generation
    if (!block) return
    event.preventDefault()
    try {
      await copyToClipboard(block.source)
      if (gen === generation) status.value = t('wiki.render.copied')
    } catch {
      if (gen === generation) status.value = t('wiki.render.copyFailed')
    }
    return
  }
  const anchor = event.target.closest('a[data-md-wiki]')
  if (anchor) {
    event.preventDefault()
    const id = doc.value.wikiLinks[Number(anchor.getAttribute('data-md-wiki'))]
    if (
      !props.disabled &&
      props.wiki?.pages.some((p) => p.id === id && p.projectId === props.wiki?.page.projectId)
    )
      emit('navigate', id)
    return
  }
  const url = markdownLinkHref(event.target)
  if (url) {
    event.preventDefault()
    openExternalURL(url)
  }
}
watch(
  () =>
    [
      props.source,
      props.wiki,
      props.profile,
      locale.value,
      props.allowLarge,
      retryVersion.value,
    ] as const,
  async () => {
    cleanup()
    imageSizes.clear()
    status.value = ''
    const gen = generation
    large.value = new TextEncoder().encode(props.source).length > 2 * 1024 * 1024
    if (large.value && !props.allowLarge) {
      emit('headings', [])
      return
    }
    currentProfile = props.profile ?? ''
    requestId = `img-${crypto.randomUUID()}`
    doc.value = prepareMarkdownDocument(props.source, props.wiki, {
      copy: t('wiki.render.copy'),
      missing: t('wiki.render.missing'),
      ambiguous: t('wiki.render.ambiguous'),
      image: t('wiki.render.image'),
    })
    for (const [i, slot] of doc.value.imageSlots.entries())
      if (slot.code) errors.set(i, imageLabel(slot.code))
    emit('headings', doc.value.headings)
    refresh()
    await nextTick()
    if (gen !== generation || !root.value) return
    bindScrollRoot()
    if (typeof ResizeObserver !== 'undefined') {
      positionObserver = new ResizeObserver(onScroll)
      positionObserver.observe(root.value)
    }
  },
  { immediate: true },
)
function bindScrollRoot() {
  if (!root.value || !doc.value) return
  scrollRoot?.removeEventListener('scroll', onScroll)
  observer?.disconnect()
  observer = undefined
  scrollRoot = getScrollRoot()
  scrollRoot?.addEventListener('scroll', onScroll, { passive: true })
  window.addEventListener('resize', onScroll)
  if (typeof IntersectionObserver !== 'undefined') {
    observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries)
          if (entry.isIntersecting) {
            const image = entry.target.getAttribute('data-md-image'),
              code = entry.target.getAttribute('data-md-code')
            if (image !== null) loadImage(Number(image))
            if (code !== null) highlight(Number(code))
            observer?.unobserve(entry.target)
          }
      },
      { root: scrollRoot === document.scrollingElement ? null : scrollRoot, rootMargin: '200px' },
    )
    observeVisible()
  }
  updatePosition()
}
watch(() => props.scrollContainer, async () => {
  await nextTick()
  bindScrollRoot()
})
watch(
  () => props.disabled,
  (disabled) => {
    if (disabled) cancelImages()
    else void pumpImages()
  },
)
onUnmounted(() => {
  disposed = true
  cleanup()
  imageSizes.clear()
})
</script>
<template>
  <div>
    <p v-if="large && !allowLarge" role="status">{{ t('wiki.render.large') }}</p>
    <pre v-if="large && !allowLarge" class="markdown-source">{{ source }}</pre>
    <!-- eslint-disable vue/no-v-html -->
    <div
      v-else
      ref="root"
      class="markdown-body"
      :class="{ 'markdown-wiki': variant === 'wiki', 'markdown-comment': variant === 'comment' }"
      @click="action"
      @keydown="action"
      @error.capture="onImageError"
    >
      <div v-for="(part,index) in html" :key="index" class="markdown-segment" v-html="part" />
    </div>
    <!-- eslint-enable vue/no-v-html -->
    <div v-if="pending.length || failed.length" class="markdown-image-actions">
      <button v-if="pending.length" type="button" @click="cancelImages">
        {{ t('wiki.render.cancelImages') }}
      </button>
      <button v-for="index in failed" :key="index" type="button" @click="retry(index)">
        {{ t('wiki.render.retry', { name: doc?.imageSlots[index]?.name }) }}
      </button>
    </div>
    <p role="status" aria-live="polite" class="markdown-status">{{ status }}</p>
  </div>
</template>
