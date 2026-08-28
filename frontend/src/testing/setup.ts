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

/**
 * happy-dom の `Node.prototype.nodeName` を、実ブラウザと同じく
 * 「サブクラスの実装へ委譲する」形に直す。
 *
 * 実ブラウザでは `nodeName` の getter は `Node.prototype` にあり、どのノードから
 * 呼んでも正しい名前(`H1` / `#text` 等)を返す。happy-dom は `Node.prototype` に
 * **空文字を返す既定の getter** を置き、`Element` / `Text` などのサブクラス側で
 * 上書きする実装になっている。
 *
 * DOMPurify は clobbering(`<input name="nodeName">` 等でプロパティを乗っ取る攻撃)
 * を避けるため、**`Node.prototype` から取り出した getter を直接呼ぶ**。happy-dom 上
 * ではこれが常に空文字を返し、すべての要素が「タグ名不明」として除去されてしまう
 * (= サニタイズ結果が実ブラウザと食い違い、Markdown レンダリングの検証にならない)。
 *
 * ここでの委譲は実ブラウザの挙動に近づけるための **テスト環境の補正** であり、
 * アプリのコードは一切これに依存しない(WebView 上では素の DOM がそのまま動く)。
 */
const nodePrototype: Node | undefined = globalThis.Node?.prototype
if (nodePrototype) {
  const base = Object.getOwnPropertyDescriptor(nodePrototype, 'nodeName')
  Object.defineProperty(nodePrototype, 'nodeName', {
    configurable: true,
    get(this: Node): string {
      // 実体のクラス(HTMLHeadingElement → … → Element / Text …)側の getter を探す
      let proto: object | null = Object.getPrototypeOf(this)
      while (proto && proto !== nodePrototype) {
        const desc = Object.getOwnPropertyDescriptor(proto, 'nodeName')
        if (desc?.get) return String(desc.get.call(this))
        proto = Object.getPrototypeOf(proto)
      }
      return base?.get ? String(base.get.call(this)) : ''
    },
  })
}
