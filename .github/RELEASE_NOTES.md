# backlog-assistant v1.3.0

課題詳細ポップアップを強化するリリースです。

**English summary**: The issue detail popup gets two upgrades. (1) On projects whose text formatting rule is Markdown, the description and comments are now rendered (headings, lists, tables, code, links), with a "Formatted / Source" toggle (the choice is remembered; the formatting rule is picked up when you sync the project list); links open in your default browser and remote images are never loaded (shown as placeholders) for security. (2) A maximize/restore toggle (the ⛶ button or double-clicking the title) expands the popup to nearly the full window; the state is remembered.

## 新機能

- **課題詳細・コメントの Markdown 整形表示**: 記法設定が Markdown のプロジェクトでは、詳細本文とコメントを整形して表示します(見出し・リスト・表・コード・リンク)
  - 「整形表示 / 原文」の切り替え付き(選択を記憶)。Backlog 記法・設定不明のプロジェクトは従来どおりの表示です
  - 安全のため、リンクは検証済みの http/https のみ既定ブラウザで開き、画像は読み込みません(URL のプレースホルダ表示)
  - 記法設定はプロジェクト一覧の同期で取り込まれます(Backlog 側で変更した場合は「プロジェクト一覧を同期」を実行してください)
- **詳細ポップアップの最大化 / 復元**: ヘッダの ⛶ ボタン、またはタイトルのダブルクリックで、ウィンドウ内の最大表示領域と元のサイズを切り替えられます(状態は次回も維持)

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
