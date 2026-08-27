# backlog-assistant v1.2.3

内部品質の改善と、一括処理の所要時間見積りの修正を行うリリースです。

**English summary**: Maintenance release. The bulk-update time estimate now matches the actual pacing (all API calls, including the pre-write conflict check, are spaced at least one second apart: one call per create, two per update in a normal run; resend matching and conflict-stopped rows vary). Large internal refactoring improves maintainability with no intended behavior changes, backed by expanded automated checks (499 tests).

## 修正

- **一括処理の所要時間見積りを実装と一致させました**: すべての API 呼び出し(書き込みだけでなく実行直前の競合確認も)を最低 1 秒間隔にし、通常実行時の見積り(新規 1 回 / 更新 2 回。再送・競合時は呼び出し数が変動します)・ユーザガイドの記述と実挙動が一致するようになりました。画面には新規・更新の内訳に基づく最低所要時間が表示されます

## 内部改善(見た目・操作は変わりません)

- 画面コンポーネント・スタイル・Excel 出力処理などの大規模なリファクタリング(保守性向上。挙動の変更はありません)
- 自動検査の拡充: 多言語カタログの検査対象の拡大・公開契約の固定テストなど(テスト計 499 件)

## 動作環境

- Windows 10/11(x64)/ macOS(Universal)
- Backlog API キー(ユーザ抽出の全件取得には管理者またはプロジェクト管理者権限)

## インストール

zip を展開し、`backlog-assistant.exe`(Windows)または `Backlog Assistant.app`(macOS)を起動してください。詳細は [ユーザガイド](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.md) / [User Guide](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.en.md) を参照してください。

**注意: 現時点では Windows / macOS とも配布バイナリはコード署名されていません。** 必ず本リポジトリの Releases からダウンロードしたものだけを利用してください。

- Windows: 初回起動時に SmartScreen の警告が出る場合があります。「詳細情報」→「実行」で起動できます。
- macOS: 署名・公証未対応のため、初回起動時に Gatekeeper にブロックされます。一度開こうとした後、「システム設定 → プライバシーとセキュリティ」下部の「このまま開く」を選択してください。

## 既知の制限

- `#CLEAR#`(担当者・期限・詳細・カスタム属性・親課題のクリア)は公式 API 仕様に明記が無いため実機検証中の機能です
- カスタム属性の「その他」直接入力(otherValue)の書き込みは未対応です
- 課題の親子関係は 2 階層(親 - 子)のみ対応です(Backlog が今後提供予定の孫課題(3 階層)は未対応)
- 一括更新・追加は Backlog のデータを変更します。初回はテスト用プロジェクトでの試行を推奨します
