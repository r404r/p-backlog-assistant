/**
 * メッセージカタログの静的検査(設計 §3.4。styleTokens.test.ts と同じ発想)。
 *
 * i18n は「キーが揃っている」「参照しているキーが実在する」が崩れた瞬間に
 * 画面へキー文字列がそのまま出る・訳が抜ける、という形で壊れる。人手のレビューでは
 * 見落とすため、ソースを読み込む静的検査として継続実行する。
 *
 * 検査内容:
 *  1. ja / en のキー集合が完全一致する
 *  2. 補間プレースホルダ(`{name}`)の集合が言語間で一致する
 *  3. 空文字列の訳が無い
 *  4. **すべての `t()` 呼び出し**が「リテラル / 定数 / 対応表経由 / 理由付き例外」のどれかである
 *  5. 参照しているカタログキーがすべて実在する
 *  6. どこからも参照されないカタログキーが無い(移行中のものは理由付きで許容)
 *
 * **正規表現ではなく AST で解析する**(Codex レビュー指摘)。
 * 行スキャンだと (a) リテラル引数の `t()` しか拾えず、動的キーの `t(key)` が
 * 静的検査をすり抜ける (b) コメント外のダミー文字列を書くだけで未使用キーを
 * 「使用済み」に偽装できる、という 2 つの穴がある。ここでは
 * `typescript` のコンパイラ API と `@vue/compiler-sfc` で
 *
 *   - `t()` / `$t()` の**呼び出しを 1 つ残らず列挙**し、第 1 引数の形を分類する
 *   - 参照キーは「`t()` のリテラル引数」と「**キー対応表**の値」だけから集める
 *
 * とすることで、両方の穴を塞ぐ。
 *
 * 動的キーの規律(設計 §3.3): `t()` の引数は文字列リテラルを原則とし、
 * 集合は const の**キー対応表**(値がすべてカタログキーのリテラルであるオブジェクト)
 * を経由する。対応表は手で登録するのではなく、その構造(モジュール直下の const・
 * 値が全てカタログキーの形)から自動的に「登録済み」とみなす — 登録漏れで検査が
 * 甘くなるのを防ぐため。どちらにも当てはまらない動的キーは
 * DYNAMIC_KEY_EXCEPTIONS へ「ファイル位置 + 理由」付きで登録する。
 */
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { parse as parseSfc } from '@vue/compiler-sfc'
import { NodeTypes, type RootNode, type TemplateChildNode } from '@vue/compiler-dom'
import ts from 'typescript'

import { CATALOG_NAMESPACES, messages, type LocaleCatalog } from './i18n'

// vitest の作業ディレクトリは frontend/(vite.config.ts のある場所)
const SRC_DIR = resolve(process.cwd(), 'src')
const LOCALES_DIR = resolve(SRC_DIR, 'locales')

/**
 * まだどこからも参照していないカタログキー(理由付きで許容する)。
 *
 * フェーズ 1 の共通文言(`common.*`)は**基盤担当が先に確定**させる決まりで
 * (設計 §5)、実際に参照するのは後続の画面変換担当になる。画面変換が済んだら
 * ここから 1 件ずつ消えていく。
 */
const PENDING_KEYS: { key: string; reason: string }[] = [
  // フェーズ 1 の画面変換完了時点で空。基盤担当が用意したが結局どの画面でも
  // 使われなかった common.action.export / common.label.profile は統合時に削除した。
]

/**
 * 文字列リテラル・定数・キー対応表のどれでもない `t()` の引数(動的キー)。
 * **ファイル位置(file)・引数の式(expression)・理由(reason)の 3 つが必須**。
 *
 * ここに載るのは「キー対応表から引いたキーを受け取って翻訳するだけの汎用関数」に
 * 限る。キー自体の妥当性は、呼び出し元の対応表が上の規律に従うことで担保される。
 */
const DYNAMIC_KEY_EXCEPTIONS: {
  file: string
  expression: string
  /**
   * 許可する**出現回数**。ファイル + 式だけで照合すると、同じファイルに
   * 後から増えた別の `t(key)` まで黙って許可されてしまうため、件数まで固定する
   * (2 回目レビュー指摘 2-c)。増減したらテストが落ち、意図的な変更として
   * この件数を更新することになる。
   */
  count: number
  reason: string
}[] = [
  {
    file: 'src/lib/columnLabels.ts',
    expression: 'path',
    count: 1,
    reason: '列ラベルの汎用翻訳。path は ISSUE_/USER_COLUMN_LABEL_KEYS(キー対応表)から引いた値',
  },
  {
    file: 'src/lib/enumLabels.ts',
    expression: 'path',
    count: 2,
    reason: '機械値の汎用翻訳。path は ACTION_/ROW_STATUS_/SYNC_MODE_LABEL_KEYS(キー対応表)から引いた値',
  },
  {
    file: 'src/lib/format.ts',
    expression: 'key',
    count: 2,
    reason: 'エラー整形の汎用翻訳ヘルパ。key は呼び出し元がリテラルまたはキー対応表から渡す',
  },
  {
    file: 'src/lib/message.ts',
    expression: 'current.key',
    count: 2,
    reason:
      '表示メッセージの保持ヘルパ(useMessage)。key は呼び出し元が setter へリテラルで渡す(その引数は参照キーとして集計している)',
  },
  {
    file: 'src/views/BulkUpdateView.vue',
    expression: 'key',
    count: 2,
    reason: '一括更新画面の汎用翻訳ヘルパ。key は同ファイルのキー対応表から引いた値',
  },
  {
    file: 'src/views/SyncStatusView.vue',
    expression: 'key',
    count: 2,
    reason: '同期状態画面の汎用翻訳ヘルパ。key は同ファイルのキー対応表から引いた値',
  },
]

// ---------------------------------------------------------------------------
// カタログの平坦化
// ---------------------------------------------------------------------------

/** ネストしたカタログを `a.b.c` 形式のキー → 文字列へ平坦化する */
function flatten(node: unknown, prefix = ''): Map<string, string> {
  const out = new Map<string, string>()
  if (typeof node === 'string') {
    out.set(prefix, node)
    return out
  }
  if (node && typeof node === 'object') {
    for (const [k, v] of Object.entries(node as Record<string, unknown>)) {
      for (const [key, value] of flatten(v, prefix ? `${prefix}.${k}` : k)) {
        out.set(key, value)
      }
    }
  }
  return out
}

const jaFlat = flatten(messages.ja as unknown as LocaleCatalog)
const enFlat = flatten(messages.en as unknown as LocaleCatalog)

/**
 * 注記キーか(`_` で始まるセグメントを含む)。
 *
 * JSON にはコメントが書けないため、カタログの方針を書き残す場所として
 * `_comment` のような注記キーを許す。表示には使わないので、未使用キー検査の
 * 対象から外す(ja / en のキー集合一致・空文字検査の対象にはする)。
 */
function isDocKey(key: string): boolean {
  return key.split('.').some((seg) => seg.startsWith('_'))
}

/** `{name}` 形式の補間プレースホルダを集める */
function placeholders(text: string): string[] {
  return [...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort()
}

/** カタログキーの形(名前空間 + 1 つ以上のセグメント) */
const KEY_SHAPE = new RegExp(
  `^(?:${CATALOG_NAMESPACES.join('|')})\\.[A-Za-z0-9_]+(?:\\.[A-Za-z0-9_]+)*$`,
)

// ---------------------------------------------------------------------------
// ソースの列挙
// ---------------------------------------------------------------------------

/** 検査対象のソース(src 配下の .ts / .vue。テストとカタログ自身は除く) */
function listSources(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (path === LOCALES_DIR) continue
      found.push(...listSources(path))
      continue
    }
    if (entry.name.endsWith('.test.ts')) continue
    if (entry.name.endsWith('.ts') || entry.name.endsWith('.vue')) found.push(path)
  }
  return found.sort()
}

/**
 * 1 ファイルを「TypeScript として解析できる断片」の列にする。
 *
 * .vue はスクリプトブロックに加えて、**テンプレート内の式**(`{{ … }}` の中身と
 * `:title="…"` 等のディレクティブの値)も断片として取り出す。テンプレートの式は
 * スクリプト AST には現れないため、ここで拾わないと `t()` の呼び出しを取りこぼす。
 */
export function toScriptFragments(file: string, source: string): string[] {
  if (!file.endsWith('.vue')) return [source]

  const { descriptor } = parseSfc(source, { filename: file })
  const fragments: string[] = []
  for (const block of [descriptor.script, descriptor.scriptSetup]) {
    if (block) fragments.push(block.content)
  }

  const expressions: string[] = []
  const walk = (node: TemplateChildNode | RootNode): void => {
    if (node.type === NodeTypes.INTERPOLATION) {
      if (node.content.type === NodeTypes.SIMPLE_EXPRESSION) expressions.push(node.content.content)
    }
    if (node.type === NodeTypes.ELEMENT) {
      for (const prop of node.props) {
        if (prop.type === NodeTypes.DIRECTIVE && prop.exp?.type === NodeTypes.SIMPLE_EXPRESSION) {
          expressions.push(prop.exp.content)
        }
      }
    }
    const children = (node as { children?: unknown }).children
    if (Array.isArray(children)) {
      for (const child of children) {
        if (child && typeof child === 'object' && 'type' in child) walk(child as TemplateChildNode)
      }
    }
  }
  if (descriptor.template?.ast) walk(descriptor.template.ast)

  // 式単体は文にならないため括弧で包む(`a ? b : c` や `x in xs` もこれで通る)
  for (const expression of expressions) fragments.push(`(${expression});`)
  return fragments
}

// ---------------------------------------------------------------------------
// AST 解析
// ---------------------------------------------------------------------------

/** `t()` 呼び出し 1 件の分類結果 */
export interface TranslateCall {
  file: string
  /** 第 1 引数のソース文字列(例外リストとの照合に使う) */
  expression: string
  /** 文字列リテラルだった場合のキー(それ以外は null) */
  literalKey: string | null
  /** 定数 / キー対応表を経由して解決できたキー(解決できなければ空) */
  resolvedKeys: string[]
  /** 第 1 引数の式に現れる識別子(対応表の到達判定の種にする) */
  argumentIdentifiers: string[]
}

interface FileAnalysis {
  /** このファイルの `t()` 呼び出し */
  calls: TranslateCall[]
  /** キー対応表(値がすべてカタログキーのリテラルである const オブジェクト)の値 */
  keyMapValues: string[]
  /**
   * **既知の翻訳経路**へ引数として渡されたカタログキーのリテラル。
   *
   * `t()` を直接呼ばずにキーを渡す経路は現状 lib/message.ts の `useMessage` だけで、
   * `const [msg, setMsg] = useMessage(t)` の **setter へ第 1 引数で渡したリテラル**
   * のみを参照として数える。任意の関数呼び出しの引数を数えると
   * `noop('common.action.retry')` の 1 行で未使用キーを偽装できてしまうため
   * (2 回目レビュー指摘 2-a)。
   */
  argumentKeys: string[]
  /** 参照として数えた setter 呼び出し(レビューできるよう呼び出し元名を残す) */
  argumentKeyCalls: { callee: string; key: string }[]
}

/**
 * `as const` / `satisfies` / 括弧を剥がして中身の式を返す。
 * キー対応表は `{ … } as const` の形で書かれることが多く、剥がさないと
 * オブジェクトリテラルとして認識できない。
 */
function unwrap(node: ts.Node): ts.Node {
  let current = node
  while (
    ts.isAsExpression(current) ||
    ts.isSatisfiesExpression(current) ||
    ts.isParenthesizedExpression(current)
  ) {
    current = current.expression
  }
  return current
}

/** 文字列リテラル(テンプレートリテラル含む)の値を取り出す */
function literalText(node: ts.Node): string | null {
  const expr = unwrap(node)
  if (ts.isStringLiteral(expr) || ts.isNoSubstitutionTemplateLiteral(expr)) return expr.text
  return null
}

/** 値としての参照か(プロパティ名・宣言名・引数名などの「名前」の位置ではないか) */
function isReferenceIdentifier(node: ts.Identifier): boolean {
  const parent = node.parent
  if (!parent) return true
  if (ts.isPropertyAccessExpression(parent) && parent.name === node) return false
  if (ts.isPropertyAssignment(parent) && parent.name === node) return false
  if ((ts.isVariableDeclaration(parent) || ts.isBindingElement(parent)) && parent.name === node) {
    return false
  }
  if (ts.isFunctionDeclaration(parent) && parent.name === node) return false
  if (ts.isParameter(parent) && parent.name === node) return false
  return true
}

/** ノード配下に現れる識別子名を集める(プロパティ名・宣言名は除く) */
function collectIdentifiers(node: ts.Node): Set<string> {
  const out = new Set<string>()
  const visit = (n: ts.Node): void => {
    if (ts.isIdentifier(n) && isReferenceIdentifier(n)) out.add(n.text)
    ts.forEachChild(n, visit)
  }
  visit(node)
  return out
}

/** 呼び出しの callee 名(`f()` / `a.b.f()` の `f`)。取れなければ null */
function calleeName(node: ts.CallExpression): string | null {
  const callee = node.expression
  if (ts.isIdentifier(callee)) return callee.text
  if (ts.isPropertyAccessExpression(callee)) return callee.name.text
  return null
}

/**
 * 関数本体を、**入れ子の関数の内部を除いて**走査する。
 *
 * 入れ子の関数(関数宣言・関数式・アロー関数)は collectFunctions が独立した
 * ノードとして別途登録するため、外側の関数がその中身まで自分のものとして
 * 数えてしまうと「入れ子の t() が外側の直接呼び出しに見える」ことになる
 * (4 回目レビュー指摘 b)。
 *
 * **開始ノード自体の判定も行う**(5 回目レビュー指摘): アロー関数の簡潔本体は
 * 式そのものが body になるため、`const outer = () => () => t(MAP.a)` では
 * body が内側のアロー関数になる。開始時に判定しないと内側の中身まで外側の
 * own-body として走査してしまい、outer を呼ぶ関数まで翻訳関数と誤判定される。
 */
function forEachOwnNode(body: ts.Node, visit: (n: ts.Node) => void): void {
  // 本体が関数そのもの(簡潔本体で関数を返す形)なら、外側の own-body は空
  if (isFunctionLike(body)) return
  const walk = (n: ts.Node): void => {
    visit(n)
    ts.forEachChild(n, (child) => {
      if (isFunctionLike(child)) return
      walk(child)
    })
  }
  walk(body)
}

/** 関数らしいノードか(宣言・式・アロー・メソッド) */
function isFunctionLike(node: ts.Node): node is ts.SignatureDeclaration & { body?: ts.Node } {
  return (
    ts.isFunctionDeclaration(node) ||
    ts.isFunctionExpression(node) ||
    ts.isArrowFunction(node) ||
    ts.isMethodDeclaration(node)
  )
}

/** 関数の名前を推定する(`function f(){}` / `const f = () => {}` / `{ f() {} }`) */
function functionName(node: ts.Node): string | null {
  if (ts.isFunctionDeclaration(node) && node.name) return node.name.text
  if (ts.isMethodDeclaration(node) && ts.isIdentifier(node.name)) return node.name.text
  const parent = node.parent
  if (parent && ts.isVariableDeclaration(parent) && ts.isIdentifier(parent.name)) {
    return parent.name.text
  }
  return null
}

/**
 * 1 つの TypeScript 断片を解析して、`t()` 呼び出しとキー対応表を集める。
 *
 * キー対応表の判定(= 自動登録の条件):
 *   const の初期化子がオブジェクトリテラルで、**プロパティが 1 つ以上あり、
 *   その値がすべてカタログキーの形をした文字列リテラル**であること。
 *   ダミー文字列を 1 つ書いただけでは条件を満たさないため、
 *   「未使用キーの偽装」には使えない。
 */
export function analyzeFragment(file: string, code: string): FileAnalysis {
  const sourceFile = ts.createSourceFile(file, code, ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS)

  /** 名前 → カタログキー(`const X = 'common.a.b'`) */
  const constKeys = new Map<string, string>()
  /** 名前 → カタログキーの配列(キー対応表) */
  const keyMaps = new Map<string, string[]>()
  /** `const [msg, setMsg] = useMessage(t)` の setter 名(既知の翻訳経路) */
  const messageSetters = new Set<string>()
  /** 変数名 → その初期化子に現れる識別子(到達関係をたどるための辺) */
  const initializerRefs = new Map<string, Set<string>>()

  // 1 周目: 定数とキー対応表を集める(宣言が使用より後にあっても拾えるようにする)
  const collectDeclarations = (node: ts.Node): void => {
    // useMessage の分割代入から setter 名を拾う
    if (
      ts.isVariableDeclaration(node) &&
      ts.isArrayBindingPattern(node.name) &&
      node.initializer &&
      ts.isCallExpression(node.initializer) &&
      ts.isIdentifier(node.initializer.expression) &&
      node.initializer.expression.text === 'useMessage'
    ) {
      const setter = node.name.elements[1]
      if (setter && ts.isBindingElement(setter) && ts.isIdentifier(setter.name)) {
        messageSetters.add(setter.name.text)
      }
    }
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer) {
      // `{ … } as const` のような形も対応表として認識する
      const init = unwrap(node.initializer)
      const text = literalText(init)
      if (text !== null && KEY_SHAPE.test(text)) {
        constKeys.set(node.name.text, text)
      } else if (ts.isObjectLiteralExpression(init) && init.properties.length > 0) {
        const values: string[] = []
        let allKeys = true
        for (const prop of init.properties) {
          if (!ts.isPropertyAssignment(prop)) {
            allKeys = false
            break
          }
          const value = literalText(prop.initializer)
          if (value === null || !KEY_SHAPE.test(value)) {
            allKeys = false
            break
          }
          values.push(value)
        }
        if (allKeys) keyMaps.set(node.name.text, values)
      }
      // 初期化子に現れる識別子を辺として持つ(`const ALL = { m: MAP }` → ALL → MAP)
      initializerRefs.set(node.name.text, collectIdentifiers(node.initializer))
    }
    ts.forEachChild(node, collectDeclarations)
  }
  collectDeclarations(sourceFile)

  // 2 周目: t() / $t() の呼び出しをすべて列挙する
  const calls: TranslateCall[] = []
  const argumentKeyCalls: { callee: string; key: string }[] = []
  const collectCalls = (node: ts.Node): void => {
    if (ts.isCallExpression(node)) {
      const callee = node.expression
      const name = ts.isIdentifier(callee)
        ? callee.text
        : ts.isPropertyAccessExpression(callee)
          ? callee.name.text
          : null

      // 既知の翻訳経路(useMessage の setter)へ第 1 引数で渡したリテラルだけを数える
      if (name !== null && messageSetters.has(name)) {
        const text = node.arguments[0] ? literalText(node.arguments[0]) : null
        if (text !== null && KEY_SHAPE.test(text)) argumentKeyCalls.push({ callee: name, key: text })
      }

      if (name === 't' || name === '$t' || name === 'globalTranslate') {
        const arg = node.arguments[0]
        const expression = arg ? arg.getText(sourceFile).trim() : ''
        const literal = arg ? literalText(arg) : null
        const resolved: string[] = []
        if (arg && literal === null) {
          if (ts.isIdentifier(arg)) {
            const constKey = constKeys.get(arg.text)
            if (constKey) resolved.push(constKey)
            const mapValues = keyMaps.get(arg.text)
            if (mapValues) resolved.push(...mapValues)
          } else if (ts.isElementAccessExpression(arg) || ts.isPropertyAccessExpression(arg)) {
            // `MAP[x]` / `MAP.x` の形。MAP がキー対応表なら、その全値を参照済みとみなす
            const base = arg.expression
            if (ts.isIdentifier(base)) {
              const mapValues = keyMaps.get(base.text)
              if (mapValues) resolved.push(...mapValues)
            }
          }
        }
        calls.push({
          file,
          expression,
          literalKey: literal,
          resolvedKeys: resolved,
          argumentIdentifiers: arg ? [...collectIdentifiers(arg)] : [],
        })
      }
    }
    ts.forEachChild(node, collectCalls)
  }
  collectCalls(sourceFile)

  // 3 周目: 対応表が「翻訳へ到達しているか」を調べる(3 回目レビュー指摘 1)。
  //
  // 以前は「ファイル内に t() が 1 つでもあり、対応表名が宣言以外で 1 回でも
  // 出てくれば全値を参照済み」としていたため、`void DEAD` の 1 行で偽装できた。
  // ここでは**到達関係**で判定する:
  //
  //   種(seed) = (a) t() / $t() の引数式に現れる識別子
  //            + (b) **翻訳関数**の本体に現れる識別子
  //   翻訳関数 = 本体に t() を直接持つ関数、またはそういう関数を**呼ぶ**関数(推移閉包)。
  //              DYNAMIC_KEY_EXCEPTIONS に載る汎用ヘルパ(columnLabel /
  //              translateAction 等)は、定義上この条件を満たすので自動的に含まれる。
  //   そこから「変数の初期化子に現れる識別子」の辺をたどって閉包を取る
  //   (`const ALL = { m: MAP }` を経由する columnLabels.ts の形に対応するため)。
  //
  // モジュール直下の `void DEAD` はどの関数本体にも属さないので種にならない。
  //
  // 呼び出しグラフの厳密化(4 回目レビュー指摘):
  //   - 辺は **CallExpression の callee** だけから作る。`void translateAction` のように
  //     識別子が現れるだけでは「呼んでいる」とみなさない。
  //   - 各関数の本体走査は **入れ子の関数の内部を除外**する(forEachOwnNode)。
  //     入れ子の関数は独立したノードとして別に登録されるため、内側の t() が
  //     外側関数の直接呼び出しに見えることはない。

  /** 関数ごとの情報。name は推定できなければ null(無名でも本体の識別子は種になる) */
  const functions: {
    name: string | null
    /** この関数**自身**の本体に t() / $t() の呼び出しがあるか(入れ子は含まない) */
    hasDirectTranslate: boolean
    /** この関数自身が呼んでいる関数名(入れ子は含まない) */
    calleeNames: Set<string>
    /** この関数自身の本体に現れる識別子(入れ子は含まない) */
    identifiers: Set<string>
  }[] = []
  const collectFunctions = (node: ts.Node): void => {
    if (isFunctionLike(node) && node.body) {
      const identifiers = new Set<string>()
      const calleeNames = new Set<string>()
      let hasDirectTranslate = false
      forEachOwnNode(node.body, (n) => {
        if (ts.isIdentifier(n) && isReferenceIdentifier(n)) identifiers.add(n.text)
        if (ts.isCallExpression(n)) {
          const nm = calleeName(n)
          if (nm !== null) {
            calleeNames.add(nm)
            if (nm === 't' || nm === '$t' || nm === 'globalTranslate') hasDirectTranslate = true
          }
        }
      })
      functions.push({ name: functionName(node), hasDirectTranslate, calleeNames, identifiers })
    }
    ts.forEachChild(node, collectFunctions)
  }
  collectFunctions(sourceFile)

  // 翻訳関数の推移閉包(t() を持つ関数 → それを **呼ぶ** 関数 → …)
  const translatingNames = new Set<string>()
  for (const f of functions) {
    if (f.hasDirectTranslate && f.name) translatingNames.add(f.name)
  }
  for (let changed = true; changed; ) {
    changed = false
    for (const f of functions) {
      if (!f.name || translatingNames.has(f.name)) continue
      if ([...f.calleeNames].some((id) => translatingNames.has(id))) {
        translatingNames.add(f.name)
        changed = true
      }
    }
  }

  /** その関数が翻訳へ関与しているか(無名関数も直接 / 呼び出し経由で判定する) */
  const isTranslating = (f: (typeof functions)[number]): boolean =>
    f.hasDirectTranslate || [...f.calleeNames].some((id) => translatingNames.has(id))

  const seeds = new Set<string>()
  // (a) t() の引数式に現れる識別子
  for (const call of calls) {
    if (call.argumentIdentifiers) for (const id of call.argumentIdentifiers) seeds.add(id)
  }
  // (b) 翻訳関数の本体(入れ子を除く)に現れる識別子
  for (const f of functions) {
    if (isTranslating(f)) {
      for (const id of f.identifiers) seeds.add(id)
    }
  }
  // 変数の初期化子をたどって閉包を取る
  for (let changed = true; changed; ) {
    changed = false
    for (const name of [...seeds]) {
      for (const ref of initializerRefs.get(name) ?? []) {
        if (!seeds.has(ref)) {
          seeds.add(ref)
          changed = true
        }
      }
    }
  }

  const keyMapValues: string[] = []
  for (const [mapName, values] of keyMaps) {
    if (seeds.has(mapName)) keyMapValues.push(...values)
  }

  return {
    calls,
    keyMapValues,
    argumentKeys: argumentKeyCalls.map((c) => c.key),
    argumentKeyCalls,
  }
}

/** ファイル単位で解析する(.vue はスクリプト + テンプレート式をまとめて 1 つに扱う) */
export function analyzeFile(file: string, source: string): FileAnalysis {
  // テンプレートの式からスクリプトの対応表を引けるよう、断片を連結して 1 度に解析する
  // (`t(THEME_LABEL_KEYS[m])` はテンプレート側、対応表の宣言はスクリプト側にある)
  const merged = toScriptFragments(file, source).join('\n;\n')
  return analyzeFragment(file, merged)
}

// ---------------------------------------------------------------------------
// 全ソースの解析結果
// ---------------------------------------------------------------------------

const ANALYSES = listSources(SRC_DIR).map((path) => {
  const file = relative(process.cwd(), path)
  return { file, ...analyzeFile(file, readFileSync(path, 'utf8')) }
})

/** すべての `t()` 呼び出し */
const ALL_CALLS = ANALYSES.flatMap((a) => a.calls)

/**
 * 参照されているカタログキー。次の 3 経路だけを数える:
 *   1. `t()` のリテラル引数(および定数 / 対応表を経由して解決できたキー)
 *   2. **使われている**キー対応表の値
 *   3. 既知の翻訳経路(useMessage の setter)へリテラルで渡したキー
 * ソース中に散らばった任意の文字列(変数への代入・配列リテラル・任意の関数の引数)は
 * 数えない。
 */
const REFERENCED_KEYS = new Set<string>()
for (const analysis of ANALYSES) {
  for (const value of analysis.keyMapValues) REFERENCED_KEYS.add(value)
  for (const value of analysis.argumentKeys) REFERENCED_KEYS.add(value)
  for (const call of analysis.calls) {
    if (call.literalKey !== null) REFERENCED_KEYS.add(call.literalKey)
    for (const key of call.resolvedKeys) REFERENCED_KEYS.add(key)
  }
}

/** 参照として数えた setter 呼び出しの一覧(レビュー用にテストへ出す) */
const ARGUMENT_KEY_CALLS = ANALYSES.flatMap((a) =>
  a.argumentKeyCalls.map((c) => ({ file: a.file, ...c })),
)

/** リテラルでも定数 / 対応表経由でもない呼び出し(= 動的キー) */
const DYNAMIC_CALLS = ALL_CALLS.filter(
  (c) => c.literalKey === null && c.resolvedKeys.length === 0,
)

/** 動的キー呼び出しを「ファイル + 式」でまとめて件数を数える */
const DYNAMIC_CALL_GROUPS = new Map<string, { file: string; expression: string; count: number }>()
for (const call of DYNAMIC_CALLS) {
  const id = `${call.file} ${call.expression}`
  const group = DYNAMIC_CALL_GROUPS.get(id)
  if (group) group.count++
  else DYNAMIC_CALL_GROUPS.set(id, { file: call.file, expression: call.expression, count: 1 })
}

/**
 * 例外に載っていない、または**件数が合わない**動的キー呼び出し。
 * 件数まで見ることで、同じファイルに後から増えた別の `t(key)` が
 * 既存の例外に紛れて許可されるのを防ぐ(2 回目レビュー指摘 2-c)。
 */
const DYNAMIC_CALL_OFFENDERS = [...DYNAMIC_CALL_GROUPS.values()]
  .map((group) => {
    const exception = DYNAMIC_KEY_EXCEPTIONS.find(
      (e) => e.file === group.file && e.expression === group.expression,
    )
    if (!exception) return `${group.file}: t(${group.expression}) — 例外に未登録(${group.count} 件)`
    if (exception.count !== group.count) {
      return `${group.file}: t(${group.expression}) — 件数が変わりました(登録 ${exception.count} 件 / 実際 ${group.count} 件)`
    }
    return null
  })
  .filter((v): v is string => v !== null)
  .sort()

// ---------------------------------------------------------------------------
// 検査
// ---------------------------------------------------------------------------

describe('カタログの構造', () => {
  it('名前空間が locales/{ja,en}/ のファイルと 1 対 1 で対応する', () => {
    for (const locale of ['ja', 'en'] as const) {
      const files = readdirSync(resolve(LOCALES_DIR, locale))
        .filter((f) => f.endsWith('.json'))
        .map((f) => f.replace(/\.json$/, ''))
        .sort()
      expect(files).toEqual([...CATALOG_NAMESPACES].sort())
      expect(Object.keys(messages[locale]).sort()).toEqual([...CATALOG_NAMESPACES].sort())
    }
  })

  it('ja / en のキー集合が完全一致する', () => {
    const jaKeys = [...jaFlat.keys()].sort()
    const enKeys = [...enFlat.keys()].sort()
    expect(enKeys.filter((k) => !jaFlat.has(k)), '日本語カタログに無いキー(英語のみ)').toEqual([])
    expect(jaKeys.filter((k) => !enFlat.has(k)), '英語カタログに無いキー(未訳)').toEqual([])
  })

  it('補間プレースホルダの集合が言語間で一致する', () => {
    const mismatched: string[] = []
    for (const [key, jaText] of jaFlat) {
      const enText = enFlat.get(key)
      if (enText === undefined) continue
      const a = placeholders(jaText)
      const b = placeholders(enText)
      if (a.join(',') !== b.join(',')) mismatched.push(`${key}: ja={${a}} en={${b}}`)
    }
    expect(mismatched).toEqual([])
  })

  it('空文字列の訳が無い', () => {
    const empty: string[] = []
    for (const [locale, flat] of [
      ['ja', jaFlat],
      ['en', enFlat],
    ] as const) {
      for (const [key, text] of flat) {
        if (text.trim() === '') empty.push(`${locale}: ${key}`)
      }
    }
    expect(empty).toEqual([])
  })
})

describe('ソースとの突合', () => {
  it('t() 呼び出しを AST で列挙できている(解析自体の健全性)', () => {
    // 取りこぼしがあると以降の検査が空振りするため、規模の下限を固定する
    expect(ALL_CALLS.length).toBeGreaterThan(100)
    expect(ALL_CALLS.some((c) => c.file.endsWith('.vue'))).toBe(true)
    expect(ALL_CALLS.some((c) => c.file.endsWith('.ts'))).toBe(true)
  })

  it('すべての t() が「リテラル / 定数 / キー対応表経由 / 理由付き例外」のどれかである', () => {
    expect(
      DYNAMIC_CALL_OFFENDERS,
      '動的キーです。キー対応表を経由させるか、理由と件数を添えて DYNAMIC_KEY_EXCEPTIONS を更新してください',
    ).toEqual([])
  })

  it('参照として数えている setter 呼び出しが既知の翻訳経路だけである', () => {
    // 任意の関数の引数を参照として数えると未使用キーを偽装できるため、
    // 数えた呼び出し(= useMessage の setter)を一覧で確認できるようにする
    for (const call of ARGUMENT_KEY_CALLS) {
      expect(
        call.callee,
        `想定外の呼び出しを参照として数えています: ${call.file} / ${call.callee}('${call.key}')`,
      ).toMatch(/^set[A-Z]/)
    }
  })

  it('t() に渡すリテラルキーがすべてカタログに実在する', () => {
    const missing = ALL_CALLS.filter((c) => c.literalKey !== null && !jaFlat.has(c.literalKey))
      .map((c) => `${c.file}: ${c.literalKey}`)
      .sort()
    expect(missing, 'カタログに無いキーを t() へ渡しています').toEqual([])
  })

  it('キー対応表の値・引数として渡されたキーがすべてカタログに実在する', () => {
    const missing: string[] = []
    for (const analysis of ANALYSES) {
      for (const value of [...analysis.keyMapValues, ...analysis.argumentKeys]) {
        if (!jaFlat.has(value)) missing.push(`${analysis.file}: ${value}`)
      }
    }
    expect([...new Set(missing)].sort(), 'カタログに無いキーを参照しています').toEqual([])
  })

  it('どこからも参照されないカタログキーが無い', () => {
    const pending = new Set(PENDING_KEYS.map((p) => p.key))
    const unused = [...jaFlat.keys()]
      .filter((k) => !isDocKey(k) && !REFERENCED_KEYS.has(k) && !pending.has(k))
      .sort()
    expect(unused, '未使用のキーです(使うか、理由を添えて PENDING_KEYS へ登録してください)').toEqual(
      [],
    )
  })

  it('PENDING_KEYS に、既に使われているキー・存在しないキーが残っていない', () => {
    const stale = PENDING_KEYS.filter((p) => REFERENCED_KEYS.has(p.key) || !jaFlat.has(p.key)).map(
      (p) => p.key,
    )
    expect(stale, '参照済み・不存在のキーは PENDING_KEYS から外してください').toEqual([])
  })

  it('PENDING_KEYS の各項目には理由が書かれている', () => {
    for (const p of PENDING_KEYS) {
      expect(p.reason.trim().length, `理由が空です: ${p.key}`).toBeGreaterThan(0)
    }
  })

  it('DYNAMIC_KEY_EXCEPTIONS は「ファイル位置 + 理由」を必ず持ち、不要な項目が残っていない', () => {
    for (const e of DYNAMIC_KEY_EXCEPTIONS) {
      expect(e.expression.trim().length, `引数の式が空です: ${e.file}`).toBeGreaterThan(0)
      expect(e.reason.trim().length, `理由が空です: ${e.file} / ${e.expression}`).toBeGreaterThan(0)
      expect(
        ALL_CALLS.some((c) => c.file === e.file && c.expression === e.expression),
        `該当する t() 呼び出しがありません(削除してください): ${e.file} / t(${e.expression})`,
      ).toBe(true)
    }
  })
})

// ---------------------------------------------------------------------------
// 解析器そのものの健全性(欠陥注入)
// ---------------------------------------------------------------------------

describe('解析器そのものの健全性', () => {
  it('リテラル引数の t() をキーとして拾う', () => {
    const { calls } = analyzeFragment('sample.ts', `t('app.nav.settings'); $t("common.action.close")`)
    expect(calls.map((c) => c.literalKey)).toEqual(['app.nav.settings', 'common.action.close'])
  })

  it('動的キーの t() を「未解決」として検出する', () => {
    const { calls } = analyzeFragment('sample.ts', `const key = someKey(); t(key)`)
    const unresolved = calls.filter((c) => c.literalKey === null && c.resolvedKeys.length === 0)
    expect(unresolved.map((c) => c.expression)).toEqual(['key'])
  })

  it('テンプレート内の動的キーも検出する(スクリプトだけを見ない)', () => {
    const sfc = [
      '<script lang="ts" setup>',
      'const label = (k: string) => k',
      '</script>',
      '<template>',
      '  <span>{{ t(label) }}</span>',
      '</template>',
    ].join('\n')
    const { calls } = analyzeFile('sample.vue', sfc)
    const unresolved = calls.filter((c) => c.literalKey === null && c.resolvedKeys.length === 0)
    expect(unresolved.map((c) => c.expression)).toEqual(['label'])
  })

  it('キー対応表経由(MAP[x])は解決済みとして扱う', () => {
    const code = [
      `const MAP = { a: 'about.theme.mode.light', b: 'about.theme.mode.dark' }`,
      `t(MAP[x])`,
    ].join('\n')
    const { calls, keyMapValues } = analyzeFragment('sample.ts', code)
    expect(keyMapValues.sort()).toEqual(['about.theme.mode.dark', 'about.theme.mode.light'])
    expect(calls[0].resolvedKeys.sort()).toEqual([
      'about.theme.mode.dark',
      'about.theme.mode.light',
    ])
  })

  it('カタログキーの定数を経由した t() も解決する', () => {
    const code = [`const UNKNOWN = 'common.state.unknown'`, `t(UNKNOWN, { value: 1 })`].join('\n')
    const { calls } = analyzeFragment('sample.ts', code)
    expect(calls[0].resolvedKeys).toEqual(['common.state.unknown'])
  })

  it('ダミー文字列を書いただけでは参照済みにならない(未使用キーの偽装を防ぐ)', () => {
    // 以前の正規表現方式では、この 1 行だけで未使用検査をすり抜けられた
    const code = [
      `// common.action.retry`,
      `const dummy = 'common.action.retry'`,
      `const list = ['common.action.reload']`,
    ].join('\n')
    const { calls, keyMapValues, argumentKeys } = analyzeFragment('sample.ts', code)
    expect(calls).toEqual([])
    expect(keyMapValues).toEqual([])
    expect(argumentKeys).toEqual([])
  })

  it('useMessage の setter へリテラルで渡したキーは参照済みとして数える', () => {
    const code = [
      `const [globalError, setGlobalError] = useMessage(t)`,
      `setGlobalError('issues.error.loadProjects', { message: e })`,
    ].join('\n')
    expect(analyzeFragment('sample.ts', code).argumentKeys).toEqual(['issues.error.loadProjects'])
  })

  it('useMessage 由来でない関数の引数は参照として数えない(偽装を防ぐ)', () => {
    // 以前は「console 以外のあらゆる呼び出しの全引数」を数えていたため、
    // この 1 行で未使用キーを「使用済み」に偽装できた(2 回目レビュー指摘 2-a)
    for (const code of [
      `noop('common.action.retry')`,
      `console.warn('common.action.retry')`,
      `doSomething(1, 'common.action.retry')`,
    ]) {
      expect(analyzeFragment('sample.ts', code).argumentKeys, code).toEqual([])
    }
  })

  it('宣言しただけで使われていないキー対応表は参照として数えない', () => {
    // 2 回目レビュー指摘 2-b。使われていない対応表で未使用キーを黙らせない
    const unused = `const DEAD = { a: 'common.action.retry' } as const; t('app.title')`
    expect(analyzeFragment('sample.ts', unused).keyMapValues).toEqual([])

    // 汎用翻訳関数へ間接的に渡している形(columnLabels.ts 等)は数える
    const used = [
      `const MAP = { a: 'common.action.retry' } as const`,
      `const ALL = { m: MAP }`,
      `function label(k: string) { return t(ALL.m[k]) }`,
    ].join('\n')
    expect(analyzeFragment('sample.ts', used).keyMapValues).toEqual(['common.action.retry'])
  })

  it('t() が 1 つも無いファイルの対応表は参照として数えない', () => {
    const code = `const MAP = { a: 'common.action.retry' } as const; export default MAP`
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual([])
  })

  it('翻訳へ到達しない参照(void DEAD 等)では参照済みにならない', () => {
    // 3 回目レビュー指摘 1。「同じファイルに t() があり名前が 1 回出てくる」だけでは不可
    const code = [
      `const DEAD = { a: 'common.action.retry' } as const`,
      `void DEAD`,
      `t('app.title')`,
    ].join('\n')
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual([])
  })

  it('使用中の別の対応表と値が重なっても、未使用の対応表は数えない', () => {
    const code = [
      `const LIVE = { a: 'common.action.retry' } as const`,
      `const DEAD = { a: 'common.action.retry', b: 'common.action.reload' } as const`,
      `void DEAD`,
      `function label(k: string) { return t(LIVE[k]) }`,
    ].join('\n')
    // LIVE の値だけが数えられ、DEAD だけが持つ common.action.reload は数えない
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual(['common.action.retry'])
  })

  it('翻訳関数名が識別子として現れるだけでは呼び出しとみなさない', () => {
    // 4 回目レビュー指摘 (a)。`void translateAction` は呼び出しではないので、
    // f は翻訳関数にならず、同じ本体にある DEAD も種にならない
    const code = [
      `const DEAD = { a: 'common.action.retry' } as const`,
      `function translateAction(k: string) { return t(k) }`,
      `function f() { void translateAction; void DEAD }`,
    ].join('\n')
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual([])
  })

  it('実際に呼び出していれば翻訳関数として扱う(呼び出し辺は callee から作る)', () => {
    const code = [
      `const MAP = { a: 'common.action.retry' } as const`,
      `function translateAction(k: string) { return t(k) }`,
      `function f(x: string) { return translateAction(MAP[x]) }`,
    ].join('\n')
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual(['common.action.retry'])
  })

  it('入れ子関数の t() は外側関数の直接呼び出しとして扱わない', () => {
    // 4 回目レビュー指摘 (b)。内側のアローだけが翻訳関数で、
    // 外側の本体にある DEAD は種にならない(内側が参照する MAP だけが数えられる)
    const code = [
      `const DEAD = { a: 'common.action.reload' } as const`,
      `const MAP = { a: 'common.action.retry' } as const`,
      `function outer(k: string) { void DEAD; return () => t(MAP[k]) }`,
    ].join('\n')
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual(['common.action.retry'])
  })

  it('簡潔本体で関数を返す形でも、内側の t() を外側の直接呼び出しにしない', () => {
    // 5 回目レビュー指摘。`() => () => t(…)` は body が内側のアロー関数になる。
    // outer 自体は翻訳関数ではないので、outer を呼ぶ caller も翻訳関数にならず、
    // caller の本体にある DEAD は種にならない。
    const code = [
      `const DEAD = { a: 'common.action.reload' } as const`,
      `const MAP = { a: 'common.action.retry' } as const`,
      `const outer = () => () => t(MAP.a)`,
      `function caller() { void DEAD; return outer() }`,
    ].join('\n')
    // 内側のアローが参照する MAP だけが数えられる
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual(['common.action.retry'])
  })

  it('汎用ヘルパへ引数で渡した対応表は参照済みとして数える(enumLabels の形)', () => {
    const code = [
      `const ACTION = { create: 'common.enum.action.create' } as const`,
      `function translate(t, keys, value) { return t(keys[value]) }`,
      `export function translateAction(t, action) { return translate(t, ACTION, action) }`,
    ].join('\n')
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual([
      'common.enum.action.create',
    ])
  })

  it('as const 付きのキー対応表も認識する', () => {
    const code = `const MAP = { a: 'common.action.close' } as const; t(MAP.a)`
    const { keyMapValues, calls } = analyzeFragment('sample.ts', code)
    expect(keyMapValues).toEqual(['common.action.close'])
    expect(calls[0].resolvedKeys).toEqual(['common.action.close'])
  })

  it('値にカタログキー以外が混ざるオブジェクトはキー対応表として扱わない', () => {
    const code = `const NOT_A_MAP = { a: 'about.title', b: 'ただの文字列' }`
    expect(analyzeFragment('sample.ts', code).keyMapValues).toEqual([])
  })
})
