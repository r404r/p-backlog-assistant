# backlog-assistant v1.2.2

課題抽出画面の使い勝手を改善するリリースです。

**English summary**: The Issues screen now shows the selected project's last sync time next to the page title ("Last synced: {datetime} ({elapsed})", or "Last synced: Not synced" for projects that have never been synced), so you can tell at a glance how fresh the local data is before searching.

## 改善

- **課題抽出のタイトル右に最終同期時刻を表示**: 見出し「課題抽出」と同じ行の右側に「最終同期: {日時} ({経過})」を表示するようになりました
  - 検索の前に、ローカルデータの鮮度がひと目で分かります
  - 未同期のプロジェクトでは「最終同期: 未同期」と表示されます
  - 同期の完了やプロジェクトの切り替えで自動的に更新されます

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
