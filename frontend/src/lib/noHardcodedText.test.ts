/**
 * ユーザ可視文字列のハードコード検査(設計 §3.4)。
 *
 * i18n 化は「変換したつもりで 1 か所だけ生の文字列が残る」という形で崩れる。
 * 正規表現の行スキャンではコメント・内部識別子・テストデータまで拾ってしまい
 * 形骸化するため、**AST 解析**で構造的に判定する:
 *
 *  - テンプレート: `@vue/compiler-sfc` で SFC を解析し、テンプレート AST の
 *    **テキストノード**と**ユーザ可視の属性**(title / aria-label / placeholder 等)を見る。
 *    コメントノード・`{{ }}` の補間・class 等の内部属性は構造的に対象外になる。
 *  - スクリプト: `typescript` で解析し、**文字列リテラル**(テンプレートリテラル含む)
 *    だけを見る。コメントは AST に現れないため自動的に除外される。
 *
 * 検出対象は日本語だけではない。**ユーザ可視の英語リテラルも禁止**する
 * (カタログ経由を強制するため)。ただしスクリプトの文字列リテラルは URL・
 * localStorage キー・列キー・CSS セレクタ等が大半で、機械的に「表示に出る」と
 * 判定することはできない。そこで英語については **UI 文らしさのヒューリスティック**
 * (下記 looksLikeUiSentence を参照)+ 理由付きの除外リストで実効性を確保する。
 *
 * 検査対象:
 *  - .vue は「変換済みファイルのリスト」(CONVERTED_FILES)。未変換の画面を最初から
 *    対象にすると常時失敗して検査の意味が無くなるため、変換のたびに追加していく。
 *  - .ts は **src 配下を自動列挙**する(テスト・カタログ JSON・生成物は除外)。
 *    ファイル単位で外す場合は FILE_EXEMPTIONS に理由付きで登録する。
 */
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { parse as parseSfc } from '@vue/compiler-sfc'
import { NodeTypes, type TemplateChildNode, type RootNode } from '@vue/compiler-dom'
import ts from 'typescript'

// vitest の作業ディレクトリは frontend/(vite.config.ts のある場所)
const SRC_DIR = resolve(process.cwd(), 'src')
const LOCALES_DIR = resolve(SRC_DIR, 'locales')

/**
 * i18n 化が済んだ .vue(frontend/ からの相対パス)。
 *
 * **画面変換の担当者へ**: 自分の画面の変換が終わったらここへ 1 行追加すること。
 * 追加した時点から、その画面に生の文字列が戻ることを検査が防ぐ。
 * 追加漏れは「src 配下の .vue が漏れなく登録されている」検査が拾う。
 */
const CONVERTED_FILES: string[] = [
  'src/App.vue',
  'src/components/BulkJobHistory.vue',
  'src/components/BulkRunConfirmation.vue',
  'src/components/CustomFieldFilters.vue',
  'src/components/IssueDetailDialog.vue',
  'src/components/SyncResultPanel.vue',
  'src/views/AboutView.vue',
  'src/views/BulkUpdateView.vue',
  'src/views/IssuesView.vue',
  'src/views/SettingsView.vue',
  'src/views/SyncStatusView.vue',
  'src/views/UsersView.vue',
]

/**
 * ファイルまるごと検査対象から外すもの。**理由が必須**。
 * 個別の文字列を外す場合は EXCLUSIONS を使う(こちらは最後の手段)。
 */
const FILE_EXEMPTIONS: { file: string; reason: string }[] = [
  {
    file: 'src/lib/backend/mock.ts',
    reason:
      'Wails 外(vite dev・テスト)で動くモックバックエンドの疑似データ(プロジェクト名・課題の件名等)を含む。これは「Backlog から届くデータ」相当で、UIカタログへ移す対象ではない',
  },
  {
    file: 'src/lib/backend/shared.ts',
    reason:
      'Goの旧契約フィールドを埋める日本語fallbackの写しを含む。画面表示はlib/enumLabels.tsで機械値から翻訳し、この値は利用しない',
  },
]

/**
 * 検査から外す個別の文字列。**ファイル位置と理由の両方が必須**(設計 §3.4)。
 * `text` は完全一致で照合する。
 */
const EXCLUSIONS: { file: string; text: string; reason: string }[] = [
  // 除外を追加する場合は { file, text, reason } を必ず 3 つとも埋めること。
  {
    file: 'src/views/BulkUpdateView.vue',
    text: '中',
    reason:
      'Backlog のマスタデータ(優先度)の名前と突合する内部の比較値。画面には出さない(既定の優先度を選ぶための判定)',
  },
]

/** ユーザに見える属性(ここに無い属性は内部指定とみなして検査しない) */
const VISIBLE_ATTRS = new Set([
  'title',
  'alt',
  'placeholder',
  'aria-label',
  'aria-description',
  'aria-placeholder',
  'aria-roledescription',
  'aria-valuetext',
])

/** 日本語(ひらがな・カタカナ・漢字・全角記号)を含むか */
function hasJapanese(text: string): boolean {
  return /[぀-ヿ㐀-䶿一-鿿＀-ﾟ]/.test(text)
}

/** ラテン文字の語(2 文字以上)を含むか。単位記号や 1 文字の記号は対象外 */
function hasLatinWord(text: string): boolean {
  return /[A-Za-z]{2,}/.test(text)
}

/**
 * 「画面に出る英語の文」らしいか(スクリプト式の英語リテラル検出のヒューリスティック)。
 *
 * 文字列リテラルは大半が内部識別子(URL・localStorage キー・列キー・CSS セレクタ・
 * イベント名・enum 値)なので、それらを構造的に外したうえで
 * **次の条件をすべて満たすものだけ**を「UI 文の疑い」とする:
 *
 *  1. 大文字のラテン文字で始まる(UI 文は文頭が大文字。識別子は camelCase / kebab-case)
 *  2. 半角スペース区切りで 2 語以上ある、または `...` / `!` / `?` で終わる
 *     (`Loading...` のような 1 語の UI 文を拾うため)
 *  3. 記号(`/ \ _ { } < > = [ ] # $ | @ ; :` と連続ピリオド以外のピリオド区切り)を含まない
 *     — URL・パス・セレクタ・書式テンプレート・ドット区切りキーを外す
 *
 * **1 語だけの英単語を UI 文候補にしない理由**(2 回目レビューでのトレードオフ判断):
 * このコードベースの非テストソースに実在する 1 語の大文字始まりリテラルは、
 * すべて内部識別子である —
 *   - `'Escape'` / `'Tab'`(lib/modalFocus.ts: KeyboardEvent.key の比較値)
 *   - `'NFKC'`(lib/backend.ts: String.prototype.normalize の引数)
 *   - `'Windows'` / `'Linux'`(lib/backend.ts: プラットフォーム判定)
 *   - `'SAMPLE'` / `'DEMO'` / `'TRIAL'` / `'MOCK'`(モックのプロジェクトキー)
 *   - `'App'`(Wails バインディングの名前空間)
 * 1 語を候補に含めると、これらが**すべて誤検知**になり除外リストが肥大化して
 * 検査自体が形骸化する。一方、1 語だけの UI 文(`Save` ボタン等)は
 * .vue テンプレートのテキスト・可視属性の検査が無条件に拾うため、
 * 実害のある取りこぼしは残らない。
 *
 * その他の限界(意図的): 小文字始まりの英文や、記号を含む英文は検出できない。
 */
export function looksLikeUiSentence(text: string): boolean {
  const trimmed = text.trim()
  if (trimmed.length < 4) return false
  if (!/^[A-Z]/.test(trimmed)) return false
  // 識別子・パス・書式に使う記号を含むものは内部文字列とみなす
  if (/[/\\_{}<>=[\]#$|@;:]/.test(trimmed)) return false
  // ドット区切り(catalog.key / file.ext / a.b.c)は内部識別子。文末の "..." は許す
  if (/\.(?!\.)[^ ]/.test(trimmed)) return false
  const multiWord = /^[A-Za-z][A-Za-z0-9'’,()%-]*(?: [A-Za-z0-9'’,().%-]+)+$/.test(trimmed)
  const sentenceEnd = /(\.{3}|[!?])$/.test(trimmed)
  return multiWord || sentenceEnd
}

/**
 * テンプレートリテラルの断片(`` `Save failed: ${e}` `` の "Save failed: ")を
 * ヒューリスティックにかけられる形へ整える。
 *
 * 断片は補間で分断されているため、末尾・先頭に連結用の区切り(`: ` / `, ` / `- `)が
 * 付く。そのままだと記号を理由に UI 文と判定されないので、連結用の区切りだけ落とす
 * (文中に埋め込まれた記号 — CSS の `a: b` 等 — は残るので誤検知は増えない)。
 */
export function normalizeTemplateChunk(text: string): string {
  return text.replace(/^[\s:,;–—-]+/, '').replace(/[\s:,;–—-]+$/, '')
}

/**
 * 大文字始まりの英単語 1 語か(`Loading` / `Failed`)。
 * looksLikeUiSentence が拾わない 1 語を、**UI 経路に限って**検出するために使う。
 */
export function looksLikeUiWord(text: string): boolean {
  return /^[A-Z][A-Za-z]{2,}$/.test(text.trim())
}

/**
 * 「1 語でも UI 文とみなしてよい」狭い文脈か(3 回目レビュー指摘 2)。
 *
 * 1 語の大文字始まりリテラルを全面的に検出すると、`'Escape'` / `'NFKC'` /
 * `'Windows'` / `'SAMPLE'` のような内部識別子がすべて誤検知になる
 * (looksLikeUiSentence の docblock を参照)。そこで **画面へ流れることが
 * 構造から言える位置**に限って 1 語も検出する:
 *
 *  (a) `ref()` / `shallowRef()` / `reactive()` の第 1 引数
 *      — 表示用の状態の初期値。`const label = ref('Loading')` → `{{ label }}`
 *  (b) `computed(() => …)` のコールバックが返す値
 *      — 表示文字列の組み立て
 *  (c) テンプレートリテラルの**先頭断片**が 1 語になるもの
 *      — `` `Loading ${count}` ``
 *
 * これらの位置に内部識別子を置く実装は現状このコードベースに無い
 * (`ref('Windows')` 等は 0 件)。将来出てきた場合は構造的除外か
 * 理由付き除外(EXCLUSIONS)で対処する。
 */
function isNarrowUiContext(node: ts.Node): boolean {
  const parent = node.parent
  if (!parent) return false

  // (c) テンプレートリテラルの先頭断片
  if (ts.isTemplateHead(node)) return true

  // (a) ref / shallowRef / reactive の第 1 引数
  if (
    ts.isCallExpression(parent) &&
    ts.isIdentifier(parent.expression) &&
    ['ref', 'shallowRef', 'reactive'].includes(parent.expression.text) &&
    parent.arguments[0] === node
  ) {
    return true
  }

  // (b) computed(() => …) の返り値
  for (let p: ts.Node | undefined = parent; p; p = p.parent) {
    if (ts.isArrowFunction(p) || ts.isFunctionExpression(p)) {
      const owner = p.parent
      const isComputedCallback =
        owner !== undefined &&
        ts.isCallExpression(owner) &&
        ts.isIdentifier(owner.expression) &&
        owner.expression.text === 'computed' &&
        owner.arguments[0] === p
      if (!isComputedCallback) return false
      // 返り値の位置か(簡潔本体、または return 文の中)
      if (ts.isArrowFunction(p) && p.body === node) return true
      for (let q: ts.Node | undefined = node.parent; q && q !== p; q = q.parent) {
        if (ts.isReturnStatement(q)) return true
        // 簡潔本体(三項演算子等)は return 文を持たないため、本体に達したら真とする
        if (ts.isArrowFunction(p) && q === p.body) return true
      }
      return false
    }
  }
  return false
}

export interface Finding {
  file: string
  where: string
  text: string
}

// ---------------------------------------------------------------------------
// テンプレート(SFC)の検査
// ---------------------------------------------------------------------------

/** テンプレート AST を歩いて、ユーザ可視のハードコード文字列を集める */
function scanTemplate(file: string, root: RootNode): Finding[] {
  const found: Finding[] = []

  const visit = (node: TemplateChildNode | RootNode): void => {
    switch (node.type) {
      case NodeTypes.COMMENT:
        // テンプレート内のコメントは画面に出ない
        return
      case NodeTypes.INTERPOLATION:
        // `{{ … }}` の中身はスクリプト式。スクリプトと同じ基準で判定する
        if (node.content.type === NodeTypes.SIMPLE_EXPRESSION) {
          found.push(
            ...scanTemplateExpression(file, node.content.content, 'テンプレートの補間式'),
          )
        }
        return
      case NodeTypes.TEXT: {
        const text = node.content.trim()
        if (text && (hasJapanese(text) || hasLatinWord(text))) {
          found.push({ file, where: 'テンプレートのテキスト', text })
        }
        return
      }
      case NodeTypes.ELEMENT: {
        for (const prop of node.props) {
          if (prop.type === NodeTypes.ATTRIBUTE) {
            const value = prop.value?.content?.trim() ?? ''
            if (!value) continue
            if (!VISIBLE_ATTRS.has(prop.name)) continue
            if (hasJapanese(value) || hasLatinWord(value)) {
              found.push({ file, where: `属性 ${prop.name}`, text: value })
            }
            continue
          }
          // v-bind / v-on 等の式。スクリプトと同じ基準で式の中のリテラルを検出する
          const exp = prop.exp
          if (exp && exp.type === NodeTypes.SIMPLE_EXPRESSION) {
            found.push(
              ...scanTemplateExpression(file, exp.content, `ディレクティブ ${prop.name} の式`),
            )
          }
        }
        for (const child of node.children) visit(child)
        return
      }
      default: {
        const children = (node as { children?: unknown }).children
        if (Array.isArray(children)) {
          for (const child of children) {
            if (child && typeof child === 'object' && 'type' in child) {
              visit(child as TemplateChildNode)
            }
          }
        }
      }
    }
  }

  visit(root)
  return found
}

// ---------------------------------------------------------------------------
// スクリプト(TypeScript)の検査
// ---------------------------------------------------------------------------

/**
 * 画面に出ようが無い位置の文字列リテラルか(日本語 / 英語のどちらにも適用する)。
 *
 *  - `console.*` の引数: 開発者向けの動作ログ。UI ではない
 *  - import / export のモジュール指定
 *  - オブジェクトのキー・プロパティ名・リテラル型
 */
function isNeverUserVisible(node: ts.Node): boolean {
  const parent = node.parent
  if (!parent) return false

  if (ts.isImportDeclaration(parent) || ts.isExportDeclaration(parent)) return true
  if (ts.isPropertyAssignment(parent) && parent.name === node) return true
  if (ts.isPropertySignature(parent) && parent.name === node) return true
  if (ts.isLiteralTypeNode(parent)) return true

  for (let p: ts.Node | undefined = parent; p; p = p.parent) {
    if (ts.isCallExpression(p)) {
      const callee = p.expression
      if (
        ts.isPropertyAccessExpression(callee) &&
        ts.isIdentifier(callee.expression) &&
        callee.expression.text === 'console'
      ) {
        return true
      }
      // 呼び出しをまたいで遡ると外側の式まで開発者向け扱いになるため、ここで打ち切る
      break
    }
  }
  return false
}

/**
 * 英語のヒューリスティックだけに適用する追加の除外。
 *
 * `throw` / `new *Error('…')` の文言は慣習的に開発者向け(不変条件の違反など)で、
 * 英語で書かれていても UI 文とは限らない。誤検知が多くなるため英語では見逃す。
 * ただし**日本語の場合は見逃さない** — 日本語の Error 文言は
 * `errorMessage(e)` 経由で画面に出ることがあるため。
 */
function isDeveloperFacingEnglish(node: ts.Node): boolean {
  for (let p: ts.Node | undefined = node.parent; p; p = p.parent) {
    if (ts.isThrowStatement(p)) return true
    if (ts.isNewExpression(p) && ts.isIdentifier(p.expression) && /Error$/.test(p.expression.text)) {
      return true
    }
    if (ts.isCallExpression(p)) break
  }
  return false
}

/**
 * TypeScript のソースから、ユーザ可視と疑われる文字列リテラルを集める。
 * 日本語は無条件、英語は looksLikeUiSentence のヒューリスティックで判定する。
 *
 * **テンプレートリテラルの断片も対象**にする(`` `Save failed: ${e}` ``)。
 * 断片は TemplateHead / TemplateMiddle / TemplateTail という別ノードになるため、
 * 文字列リテラルだけを見ていると素通りする(2 回目レビュー指摘 1-b)。
 *
 * @param where 指摘に付ける場所の名前(スクリプト / テンプレートの式)
 */
function scanScript(file: string, code: string, where = 'スクリプト'): Finding[] {
  const found: Finding[] = []
  const sourceFile = ts.createSourceFile(file, code, ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS)

  const visit = (node: ts.Node): void => {
    const isChunk =
      ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node)
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || isChunk) {
      const text = node.text
      // テンプレート断片は補間で分断されているため、連結用の区切りを落としてから判定する
      const forHeuristic = isChunk ? normalizeTemplateChunk(text) : text
      if (isNeverUserVisible(node)) {
        // 動作ログ・モジュール指定・キーは対象外
      } else if (hasJapanese(text)) {
        found.push({ file, where: `${where}の文字列リテラル`, text: text.trim() })
      } else if (looksLikeUiSentence(forHeuristic) && !isDeveloperFacingEnglish(node)) {
        found.push({ file, where: `${where}の英語リテラル(UI 文の疑い)`, text: text.trim() })
      } else if (
        // 1 語の英単語は、画面へ流れることが構造から言える位置だけ検出する
        looksLikeUiWord(forHeuristic) &&
        isNarrowUiContext(node) &&
        !isDeveloperFacingEnglish(node)
      ) {
        found.push({ file, where: `${where}の英語リテラル(UI 語の疑い)`, text: text.trim() })
      }
    }
    ts.forEachChild(node, visit)
  }

  visit(sourceFile)
  return found
}

/**
 * テンプレート内の式(`{{ … }}` の中身・`:title="…"` の値)を検査する。
 *
 * 式は Vue のテンプレート AST では文字列のまま持たれているので、そのまま
 * TypeScript として解析し直してスクリプトと**同じ基準**で判定する。
 * 以前は式全体に対する日本語判定しか行っておらず、`:title="'Save failed'"` の
 * ような英語リテラルが素通りしていた(2 回目レビュー指摘 1-a)。
 */
function scanTemplateExpression(file: string, expression: string, where: string): Finding[] {
  // 式単体は文にならないため括弧で包む(`a ? b : c` や `x in xs` もこれで通る)
  return scanScript(file, `(${expression});`, where)
}

// ---------------------------------------------------------------------------
// 1 ファイルの検査
// ---------------------------------------------------------------------------

/** 除外リストを適用する前の生の指摘 */
function rawScan(file: string, source: string): Finding[] {
  const found: Finding[] = []
  if (file.endsWith('.vue')) {
    const { descriptor, errors } = parseSfc(source, { filename: file })
    expect(errors, `${file} の解析に失敗しました`).toEqual([])
    if (descriptor.template?.ast) found.push(...scanTemplate(file, descriptor.template.ast))
    for (const block of [descriptor.script, descriptor.scriptSetup]) {
      if (block) found.push(...scanScript(file, block.content))
    }
  } else {
    found.push(...scanScript(file, source))
  }
  return found
}

/** ファイルを検査して、除外リストに載っていない指摘を返す */
export function scanFile(file: string, source: string): Finding[] {
  return rawScan(file, source).filter(
    (f) => !EXCLUSIONS.some((e) => e.file === f.file && e.text === f.text),
  )
}

// ---------------------------------------------------------------------------
// 検査対象の列挙
// ---------------------------------------------------------------------------

/** src 配下の .vue を再帰的に列挙する(CONVERTED_FILES の登録漏れ検出に使う) */
function listVueFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      found.push(...listVueFiles(path))
      continue
    }
    if (!entry.name.endsWith('.vue')) continue
    found.push(relative(process.cwd(), path))
  }
  return found.sort()
}

/** src 配下の .ts を再帰的に列挙する(テスト・カタログ・型宣言は除く) */
function listScriptFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (path === LOCALES_DIR) continue
      found.push(...listScriptFiles(path))
      continue
    }
    if (!entry.name.endsWith('.ts')) continue
    if (entry.name.endsWith('.test.ts')) continue
    if (entry.name.endsWith('.d.ts')) continue
    found.push(relative(process.cwd(), path))
  }
  return found.sort()
}

const EXEMPT_FILES = new Set(FILE_EXEMPTIONS.map((e) => e.file))
const SCRIPT_FILES = listScriptFiles(SRC_DIR).filter((f) => !EXEMPT_FILES.has(f))
const VUE_FILES = listVueFiles(SRC_DIR).filter((f) => !EXEMPT_FILES.has(f))
const TARGET_FILES = [...CONVERTED_FILES, ...SCRIPT_FILES]

// ---------------------------------------------------------------------------
// 検査
// ---------------------------------------------------------------------------

describe('ユーザ可視文字列のハードコード', () => {
  it('src 配下の .ts を取りこぼさず列挙している(列挙自体の健全性)', () => {
    expect(SCRIPT_FILES).toContain('src/main.ts')
    expect(SCRIPT_FILES).toContain('src/lib/format.ts')
    expect(SCRIPT_FILES).toContain('src/lib/theme.ts')
    expect(SCRIPT_FILES.some((f) => f.endsWith('.test.ts'))).toBe(false)
    expect(SCRIPT_FILES.some((f) => f.startsWith('src/locales/'))).toBe(false)
    expect(SCRIPT_FILES.length).toBeGreaterThan(10)
  })

  it('src 配下の .vue が CONVERTED_FILES に漏れなく登録されている(登録漏れの防止)', () => {
    // 画面を分割して component を切り出したとき、CONVERTED_FILES への追加を
    // 忘れると、その component だけ静かに検査対象から外れる(実際に
    // src/components/ の 5 件が漏れていた)。自動列挙と突き合わせて気付けるようにする。
    // 未変換の .vue を一時的に外す場合は FILE_EXEMPTIONS へ理由付きで登録する。
    expect([...CONVERTED_FILES].sort()).toEqual(VUE_FILES)
  })

  it.each(TARGET_FILES)('%s に生のユーザ可視文字列が残っていない', (file) => {
    const source = readFileSync(resolve(SRC_DIR, '..', file), 'utf8')
    const findings = scanFile(file, source).map((f) => `${f.where}: ${JSON.stringify(f.text)}`)
    expect(findings, `${file} はカタログ経由に置き換えてください`).toEqual([])
  })

  it('ファイル単位の除外は理由を持ち、不要になった項目が残っていない', () => {
    for (const e of FILE_EXEMPTIONS) {
      expect(e.reason.trim().length, `理由が空です: ${e.file}`).toBeGreaterThan(0)
      const source = readFileSync(resolve(SRC_DIR, '..', e.file), 'utf8')
      // 除外を外しても指摘が出ないなら、そのファイルは既に変換済み。
      // 放置すると検査対象から静かに漏れ続けるため、テストで気付けるようにする。
      expect(
        rawScan(e.file, source).length,
        `除外が不要になっています(削除して検査対象に戻してください): ${e.file}`,
      ).toBeGreaterThan(0)
    }
  })

  it('除外リストは「ファイル位置 + 理由」を必ず持つ', () => {
    for (const e of EXCLUSIONS) {
      expect(TARGET_FILES, `除外の対象が検査対象に含まれていません: ${e.file}`).toContain(e.file)
      expect(e.text.length, `除外の対象文字列が空です: ${e.file}`).toBeGreaterThan(0)
      expect(e.reason.trim().length, `除外の理由が空です: ${e.file} / ${e.text}`).toBeGreaterThan(0)
    }
  })

  it('除外リストに、既に不要になった項目が残っていない', () => {
    for (const e of EXCLUSIONS) {
      const source = readFileSync(resolve(SRC_DIR, '..', e.file), 'utf8')
      // 除外を外した状態で検査し、その文字列が実際に検出されることを確かめる
      expect(
        rawScan(e.file, source).some((f) => f.text === e.text),
        `除外が不要になっています(削除してください): ${e.file} / ${e.text}`,
      ).toBe(true)
    }
  })
})

describe('検査器そのものの健全性', () => {
  const sample = [
    '<script lang="ts" setup>',
    '// これはコメントなので検出しない',
    "const key = 'ba.sidebarCollapsed'",
    "const message = '保存に失敗しました'",
    "const english = 'Save failed'",
    'const templated = `Export failed: ${reason}`',
    '</script>',
    '<template>',
    '  <!-- コメントは検出しない -->',
    '  <div class="panel" title="ドラッグで移動">',
    '    未変換の日本語',
    '    Hardcoded English',
    '    {{ t(\'app.title\') }}',
    "    {{ ok ? 'Connection succeeded' : '' }}",
    '    <span :aria-label="label">{{ value }}</span>',
    '    <button :title="\'Retry the sync\'">x</button>',
    '  </div>',
    '</template>',
  ].join('\n')

  const findings = scanFile('sample.vue', sample)
  const texts = findings.map((f) => f.text)

  // テキストノードは改行を挟んで 1 つにまとまるため、部分一致で確かめる
  const joined = texts.join('\n')

  it('テンプレートの生の日本語を検出する', () => {
    expect(joined).toContain('未変換の日本語')
  })

  it('テンプレートの生の英語も検出する(カタログ経由を強制する)', () => {
    expect(joined).toContain('Hardcoded English')
  })

  it('ユーザ可視の属性に書かれた日本語を検出する', () => {
    expect(texts).toContain('ドラッグで移動')
  })

  it('スクリプトの日本語リテラルを検出する', () => {
    expect(texts).toContain('保存に失敗しました')
  })

  it('テンプレートの補間式の中の英語リテラルを検出する', () => {
    expect(texts).toContain('Connection succeeded')
  })

  it('ディレクティブの式の中の英語リテラルを検出する', () => {
    expect(texts).toContain('Retry the sync')
  })

  it('テンプレートリテラルの断片も検査する(補間で分断されていても拾う)', () => {
    expect(texts.map((t) => t.trim())).toContain('Export failed:')
  })

  it('スクリプトの英語 UI 文リテラルも検出する', () => {
    expect(texts).toContain('Save failed')
  })

  it('コメント・内部識別子・補間・class 属性は検出しない', () => {
    expect(joined).not.toContain('これはコメントなので検出しない')
    expect(joined).not.toContain('コメントは検出しない')
    expect(joined).not.toContain('ba.sidebarCollapsed')
    expect(joined).not.toContain('panel')
    expect(joined).not.toContain("t('app.title')")
  })
})

describe('1 語の英語 UI 語(狭い文脈に限った検出)', () => {
  /** 断片を .ts として検査したときの指摘文字列 */
  function scan(code: string): string[] {
    return scanFile('sample.ts', code).map((f) => f.text)
  }

  it('ref / shallowRef / reactive の初期値は 1 語でも検出する', () => {
    expect(scan(`const label = ref('Loading')`)).toContain('Loading')
    expect(scan(`const label = shallowRef('Failed')`)).toContain('Failed')
    expect(scan(`const state = reactive('Pending')`)).toContain('Pending')
  })

  it('computed の返り値は 1 語でも検出する', () => {
    expect(scan(`const label = computed(() => 'Loading')`)).toContain('Loading')
    expect(scan(`const label = computed(() => { return 'Loading' })`)).toContain('Loading')
    expect(scan(`const label = computed(() => (ok ? 'Done' : 'Failed'))`)).toContain('Done')
  })

  it('テンプレートリテラルの先頭断片は 1 語でも検出する', () => {
    expect(scan('const m = `Loading ${count}`').map((t) => t.trim())).toContain('Loading')
  })

  it('狭い文脈の外にある 1 語の内部識別子は検出しない(誤検知を避ける)', () => {
    // 実在する内部識別子(modalFocus.ts / backend.ts)。全面検出だとすべて誤検知になる
    for (const code of [
      `if (e.key === 'Escape') close()`,
      `if (e.key === 'Tab') trap()`,
      `const s = raw.normalize('NFKC')`,
      `if (platform === 'Windows') syncTitleBar()`,
      `const keys = ['SAMPLE', 'DEMO', 'TRIAL']`,
    ]) {
      expect(scan(code), code).toEqual([])
    }
  })

  it('狭い文脈でも 2 文字以下・小文字始まりは検出しない', () => {
    expect(scan(`const mode = ref('ja')`)).toEqual([])
    expect(scan(`const mode = ref('auto')`)).toEqual([])
  })
})

describe('英語 UI 文のヒューリスティック', () => {
  it('画面に出そうな英文を UI 文とみなす', () => {
    for (const text of [
      'Save failed',
      'No results found',
      'Loading...',
      'Connection test succeeded',
      'Are you sure?',
    ]) {
      expect(looksLikeUiSentence(text), `UI 文として検出されるべき: ${text}`).toBe(true)
    }
  })

  it('内部識別子・URL・セレクタ・書式は UI 文とみなさない', () => {
    for (const text of [
      'ba.sidebarCollapsed',
      'https://github.com/r404r/p-backlog-assistant',
      'input[type="radio"]',
      'prefers-color-scheme: dark',
      'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      'common.action.close',
      'languagechange',
      'GET',
      'issueKey',
      '{count} items',
      'USER_GUIDE.en.md',
    ]) {
      expect(looksLikeUiSentence(text), `内部文字列として無視されるべき: ${text}`).toBe(false)
    }
  })
})
