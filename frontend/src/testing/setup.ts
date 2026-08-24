/**
 * Vitest 共通セットアップ。
 *
 * Node 26 の実験的 Web Storage は永続化ファイル未指定時に利用できず、
 * happy-dom が用意するブラウザ相当の localStorage より優先される。
 * アプリが必要とする標準 Storage 契約をテスト環境内で明示的に保証し、
 * Node の起動フラグや実験機能の有無にテスト結果を依存させない。
 */
function createMemoryStorage(): Storage {
  const values = new Map<string, string>()

  return {
    get length() {
      return values.size
    },
    clear() {
      values.clear()
    },
    getItem(key: string) {
      return values.get(String(key)) ?? null
    },
    key(index: number) {
      return [...values.keys()][index] ?? null
    },
    removeItem(key: string) {
      values.delete(String(key))
    },
    setItem(key: string, value: string) {
      values.set(String(key), String(value))
    },
  }
}

// 既存値を読むだけでも Node 26 の getter は警告を出すため、判定せず上書きする。
// worker ごとに新しいインスタンスを作ることで、テストファイル間も隔離される。
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: createMemoryStorage(),
})
