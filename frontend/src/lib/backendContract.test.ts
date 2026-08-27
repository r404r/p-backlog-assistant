/**
 * backend facade(lib/backend.ts)の公開契約の固定テスト。
 *
 * backend.ts は画面側の import 経路を安定させるための facade で、実体は
 * lib/backend/ 配下(contract / wails / mock / shared)へ分割されている。
 * この構造は、分割・移設のたびに **再エクスポートの 1 行が抜けても
 * 使っていない画面ではコンパイルが通ってしまう** という形で静かに壊れる。
 *
 * そこで公開面を「名前の一覧」として固定する:
 *
 *  - **型を含む契約全体**は TypeScript の AST で backend.ts をたどって集める。
 *    型は実行時に消えるため、`import * as` では検査できない(型だけが
 *    落ちた場合に気付けない)。`export * from './backend/contract'` の先も
 *    再帰的にたどるので、contract.ts 側の増減もここに現れる。
 *  - **実行時に値として存在すること**は `import * as` の列挙で確かめる。
 *    AST 上は export に見えても、実体が無ければ画面は実行時に壊れる。
 *
 * どちらかの一覧を意図して変える場合(公開 API の追加・削除)は、
 * このファイルの配列を同じコミットで更新すること。テストが落ちること自体が
 * 「公開面を変えた」という合図になる。
 */
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import ts from 'typescript'
import * as backend from './backend'

// vitest の作業ディレクトリは frontend/(vite.config.ts のある場所)
const ENTRY = resolve(process.cwd(), 'src/lib/backend.ts')

/**
 * facade が公開する名前の全体(型・関数・定数)。アルファベット順。
 *
 * 大半は lib/backend/contract.ts のデータ契約(Go 側 app.go と 1 対 1)。
 * 末尾の関数群が facade 自身の責務(バックエンド選択・イベント購読)。
 */
const PUBLIC_CONTRACT: string[] = [
  'AppVersion',
  'Backend',
  'BulkImportResult',
  'BulkJobRow',
  'BulkJobRowDetail',
  'BulkPreviewRow',
  'BulkProgress',
  'BulkRunResult',
  'BulkValidationError',
  'CUSTOM_COLUMN_PREFIX',
  'ConnectionTestResult',
  'CustomFieldDef',
  'CustomFieldFilter',
  'CustomFieldItem',
  'ExportColumn',
  'ExportResult',
  'FilterOptions',
  'IssueComment',
  'IssueCustomField',
  'IssueDetail',
  'IssueQuery',
  'IssueRow',
  'IssueSearchResult',
  'LogInfo',
  'MasterData',
  'MasterItem',
  'PermissionStatus',
  'Profile',
  'ProfileInput',
  'Project',
  'RateLimitCategory',
  'RateLimitStatus',
  'STORAGE_MODES',
  'StorageDatabase',
  'StorageInfo',
  'StorageMode',
  'SyncMode',
  'SyncPhase',
  'SyncProgress',
  'SyncResult',
  'SyncStateRow',
  'UserQuery',
  'UserRow',
  'UserSearchResult',
  'actionLabel',
  'copyToClipboard',
  'customColumnKey',
  'formatSyncProgress',
  'getBackend',
  'isMockBackend',
  'newSyncRunId',
  'onBulkProgress',
  'onSyncProgress',
  'openExternalURL',
  'rowStatusLabel',
]

/**
 * 実行時に値として存在する export(PUBLIC_CONTRACT のうち型でないもの)。
 * インタフェース・型エイリアスは実行時に消えるため、ここには載らない。
 */
const RUNTIME_EXPORTS: string[] = [
  'CUSTOM_COLUMN_PREFIX',
  'STORAGE_MODES',
  'actionLabel',
  'copyToClipboard',
  'customColumnKey',
  'formatSyncProgress',
  'getBackend',
  'isMockBackend',
  'newSyncRunId',
  'onBulkProgress',
  'onSyncProgress',
  'openExternalURL',
  'rowStatusLabel',
]

/** 相対 import の指定子をファイルパスへ解決する(lib 配下は .ts のみ) */
function resolveModule(fromFile: string, specifier: string): string {
  return resolve(dirname(fromFile), `${specifier}.ts`)
}

/** 宣言に export 修飾子が付いているか */
function isExported(node: ts.Statement): boolean {
  if (!ts.canHaveModifiers(node)) return false
  return (ts.getModifiers(node) ?? []).some((m) => m.kind === ts.SyntaxKind.ExportKeyword)
}

/**
 * モジュールが公開する名前を集める(型を含む)。
 * `export * from './x'` は再エクスポート元を再帰的にたどる。
 */
function collectExports(file: string, seen = new Set<string>()): string[] {
  if (seen.has(file)) return [] // 相互 re-export で無限再帰しないようにする
  seen.add(file)

  const source = ts.createSourceFile(
    file,
    readFileSync(file, 'utf8'),
    ts.ScriptTarget.ESNext,
    true,
    ts.ScriptKind.TS,
  )
  const names: string[] = []
  for (const stmt of source.statements) {
    if (ts.isExportDeclaration(stmt)) {
      // export { a, b } / export { a } from './x'(名前を明示した再エクスポート)
      if (stmt.exportClause && ts.isNamedExports(stmt.exportClause)) {
        for (const el of stmt.exportClause.elements) names.push(el.name.text)
        continue
      }
      // export * from './x'(モジュールごとの再エクスポート)
      if (!stmt.exportClause && stmt.moduleSpecifier && ts.isStringLiteral(stmt.moduleSpecifier)) {
        names.push(...collectExports(resolveModule(file, stmt.moduleSpecifier.text), seen))
      }
      continue
    }
    if (!isExported(stmt)) continue
    if (ts.isVariableStatement(stmt)) {
      for (const decl of stmt.declarationList.declarations) {
        if (ts.isIdentifier(decl.name)) names.push(decl.name.text)
      }
      continue
    }
    if (
      ts.isFunctionDeclaration(stmt) ||
      ts.isClassDeclaration(stmt) ||
      ts.isInterfaceDeclaration(stmt) ||
      ts.isTypeAliasDeclaration(stmt) ||
      ts.isEnumDeclaration(stmt)
    ) {
      if (stmt.name) names.push(stmt.name.text)
    }
  }
  return names
}

const EXPORTED_NAMES = [...new Set(collectExports(ENTRY))].sort()

describe('backend facade の公開契約', () => {
  it('公開する名前(型を含む)が固定の一覧と一致する', () => {
    expect(EXPORTED_NAMES).toEqual([...PUBLIC_CONTRACT].sort())
  })

  it('実行時に値として存在する export が一覧と一致する', () => {
    expect(Object.keys(backend).sort()).toEqual([...RUNTIME_EXPORTS].sort())
  })

  it('実行時 export は公開契約の部分集合である(一覧どうしの整合)', () => {
    for (const name of RUNTIME_EXPORTS) {
      expect(PUBLIC_CONTRACT, `PUBLIC_CONTRACT に載っていません: ${name}`).toContain(name)
    }
  })

  it('内部専用の実装は facade から漏れていない', () => {
    // wails adapter / mock / shared の内部ヘルパは画面から直接使わせない
    // (使わせると facade を迂回した依存が増え、分割の意味が失われる)
    for (const internal of [
      'createWailsBackend',
      'findWailsApp',
      'findWailsRuntime',
      'findWailsRuntimeObject',
      'createMockBackend',
      'onMockBulkProgress',
      'onMockSyncProgress',
      'ACTION_LABELS',
    ]) {
      expect(EXPORTED_NAMES, `内部実装が公開されています: ${internal}`).not.toContain(internal)
    }
  })
})
