# backlog-assistant v1.3.1

課題詳細ポップアップの表示不具合を修正するリリースです。

**English summary**: Bug-fix release. In the maximized issue detail popup, the header (title area) and footer (action buttons) no longer show spurious vertical scrollbars caused by sub-pixel height rounding. On very short windows or high zoom, the popup now falls back to a single scroll for the whole dialog so every control stays reachable.

## 修正

- **最大化中のヘッダ・フッタに不要な縦スクロールバーが出ることがある問題を修正**: タイトル部分と機能ボタン部分はスクロールしない自然な高さになりました
  - 極端に低いウィンドウや高いズーム倍率では、ポップアップ全体が 1 本のスクロールに切り替わり、すべての操作を行えます

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
