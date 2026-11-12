# backlog-assistant v1.5.0

Wiki の同期・閲覧・検索・添付込みエクスポートと、単ページ編集・新規作成を追加するリリースです。v1.3.1 以降の変更をまとめて含みます(v1.4.0 の配布はありません)。

**English summary**: This release includes all changes since v1.3.1; v1.4.0 was not distributed. Sync Wiki pages separately from issues, browse a `/` hierarchy, search names and content locally with Japanese and AND/OR support, and export a project or subtree with attachments, `index.xlsx`, and `manifest.json`. Search and the tree move to a collapsible right panel. Edit or create one page at a time, switch between text and preview, and confirm before leaving unsaved changes. Detected conflicts offer discard/reload, copy your content, or overwrite after confirmation. Email notifications default to off; uncertain saves are checked by reading without automatic resends, and uncertain creation records survive restarts. Markdown tables, blank-line spacing, and line breaks improve across Wiki pages, issue details, and comments; issue-detail links also support keyboard activation. The local database upgrades to schema v7. Wiki bulk writes, deletion, tag editing, attachment uploads, automatic periodic sync, and notation conversion are unsupported. Conflict checking leaves a short check-to-save race because the API has no optimistic locking. Under Backlog's policy change, spaces started on or after July 14, 2026 do not offer Wiki; new projects in existing spaces default to Wiki off and can enable it in project settings.

## 新機能

- **Wiki の同期・閲覧・全文検索**: 課題とは別にプロジェクトごとに手動同期し、新規・更新ページの本文を取得します。`/` 区切りの階層ツリー、ページ名・本文のローカル全文検索(日本語・複数語 AND/OR)、タグ・更新情報・添付一覧、単ページの最新取得、ページ URL のコピーに対応します。
- **添付込みエクスポート**: プロジェクト全体または選択階層を、ページ本文・添付ファイル・`index.xlsx`・`manifest.json` として出力します。件数・サイズ・所要時間の事前確認、キャンセル、安全なファイル名への置換に対応し、添付の個別失敗は索引に記録して残りを続行します。
- **Wiki の単ページ編集・新規作成**: ページ名・本文を編集でき、テキスト / プレビュー切替と未保存時の離脱確認に対応します。保存前に更新日時を再取得し、競合時は「破棄して最新を読み込む」「自分の内容をコピー」「確認して上書き保存」から選べます。メール通知は既定 OFF です。
- **保存結果が不確定な場合の確認**: タイムアウト等では自動再送せず、読み取りで結果を確認します。新規作成の不確定な試行は再起動後も記録を保持し、二重作成を防ぎます。

## 改善・修正

- **Markdown 表示の改善**: Wiki・課題詳細・コメントで、表の罫線・横スクロール、空行による段落間隔、単一改行の表示を改善しました。古いテンプレートの表で区切り行だけ列が不足する場合は、見出しと直後のデータ行の列数が一致するときに表示上だけ補います。原文や出力ファイルは変更しません。
- **Wiki の検索・ツリーを右端へ移動**: パネルを折りたたんで本文を全幅に広げられます。選択ページ・検索条件を維持し、開閉状態を保存します。
- **Markdown リンクのキーボード操作**: 課題詳細のリンクを Tab で選択し、Enter で既定ブラウザに開けるようにしました。
- **ローカル DB をスキーマ v7 へ更新**: Wiki 本文・添付メタデータ・全文検索用のキャッシュを追加します。添付の実体は DB に保存せず、エクスポート時に取得します。

## 動作環境

- Windows 10/11(x64)/ macOS(Universal)
- Backlog API キー(ユーザ抽出の全件取得には管理者またはプロジェクト管理者権限)

## インストール

zip を展開し、`backlog-assistant.exe`(Windows)または `Backlog Assistant.app`(macOS)を起動してください。詳細は [ユーザガイド](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.md) / [User Guide](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.en.md) を参照してください。

**注意: 現時点では Windows / macOS とも配布バイナリはコード署名されていません。** 必ず本リポジトリの Releases からダウンロードしたものだけを利用してください。

- Windows: 初回起動時に SmartScreen の警告が出る場合があります。「詳細情報」→「実行」で起動できます。
- macOS: 署名・公証未対応のため、初回起動時に Gatekeeper にブロックされます。一度開こうとした後、「システム設定 → プライバシーとセキュリティ」下部の「このまま開く」を選択してください。

## 既知の制限

- Wiki の書き込みは単ページ編集・新規作成のみです。一括書き戻し、削除、タグ編集、添付アップロード、自動定期同期には対応していません。
- Backlog 記法と Markdown の相互変換は行いません。プロジェクトの記法設定に従って表示・出力します。
- 保存前の競合確認と実際の保存の間には短い競合窓が残ります(API に楽観ロックがないため)。その間の他者更新を完全には防げません。
- [Backlog の Wiki 提供方針変更](https://backlog.com/ja/blog/backlog-update-wiki-end-of-support-new-spaces/)により、2026-07-14 以降に新規利用を開始したスペースでは Wiki を利用できません。既存スペースの新規プロジェクトでは既定 OFF ですが、プロジェクト設定で有効にできます。
- `#CLEAR#`(担当者・期限・詳細・カスタム属性・親課題のクリア)は公式 API 仕様に明記が無いため実機検証中の機能です
- カスタム属性の「その他」直接入力(otherValue)の書き込みは未対応です
- 課題の親子関係は 2 階層(親 - 子)のみ対応です(Backlog が今後提供予定の孫課題(3 階層)は未対応)
- 一括更新・追加は Backlog のデータを変更します。初回はテスト用プロジェクトでの試行を推奨します
