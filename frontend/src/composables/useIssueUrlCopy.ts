import { computed, nextTick, onUnmounted, ref, type ComputedRef, type Ref } from 'vue'
import { copyToClipboard } from '../lib/backend'
import { issueUrl } from '../lib/backlogUrl'
import type { TranslateFn } from '../lib/columnLabels'
import { errorMessage } from '../lib/format'
import { useMessage, type MessageSetter } from '../lib/message'

interface IssueUrlCopyDependencies {
  copy?: (text: string) => Promise<void>
  toastMs?: number
}

export interface IssueUrlCopyState {
  canCopy: ComputedRef<boolean>
  toastKey: Ref<string>
  listError: ComputedRef<string>
  detailError: ComputedRef<string>
  copyIssueUrl(issueKey: string, inDetail?: boolean): Promise<void>
  clearListFeedback(): void
  clearDetailError: MessageSetter
  invalidateAndClearDetail(): void
}

/**
 * 課題 URL のコピーと、一覧／詳細それぞれの通知状態を所有する。
 * 非同期完了順が逆転した場合は最後の要求だけを反映し、画面破棄時にタイマーを残さない。
 */
export function useIssueUrlCopy(
  spaceUrl: Readonly<Ref<string>>,
  t: TranslateFn,
  dependencies: IssueUrlCopyDependencies = {},
): IssueUrlCopyState {
  const copy = dependencies.copy ?? copyToClipboard
  const toastMs = dependencies.toastMs ?? 2000
  const canCopy = computed(() => !!spaceUrl.value)
  const toastKey = ref('')
  const [listError, setListError] = useMessage(t)
  const [detailError, setDetailError] = useMessage(t)
  let timer: ReturnType<typeof setTimeout> | null = null
  let requestSequence = 0

  function clearToast() {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
    toastKey.value = ''
  }

  function clearListFeedback() {
    clearToast()
    setListError(null)
  }

  function invalidateAndClearDetail() {
    requestSequence++
    setDetailError(null)
  }

  async function copyIssueUrl(issueKey: string, inDetail = false) {
    const url = issueUrl(spaceUrl.value, issueKey)
    if (!url) return
    const sequence = ++requestSequence
    try {
      await copy(url)
      if (sequence !== requestSequence) return
      setListError(null)
      setDetailError(null)
      clearToast()
      // 同じキーの連続コピーでも role=status のDOM変更を発生させる。
      await nextTick()
      if (sequence !== requestSequence) return
      toastKey.value = issueKey
      timer = setTimeout(() => {
        toastKey.value = ''
        timer = null
      }, toastMs)
    } catch (error) {
      if (sequence !== requestSequence) return
      clearToast()
      const params = { message: errorMessage(error) }
      if (inDetail) {
        setDetailError('issues.error.copyUrl', params)
      } else {
        setListError('issues.error.copyUrl', params)
      }
    }
  }

  onUnmounted(() => {
    requestSequence++
    clearToast()
  })

  return {
    canCopy,
    toastKey,
    listError,
    detailError,
    copyIssueUrl,
    clearListFeedback,
    clearDetailError: setDetailError,
    invalidateAndClearDetail,
  }
}
