# backlog-assistant v1.6.0

画面全体の外観を Pop スタイルへ統一するリリースです。v1.5.0 以降の変更を含みます。システム / ライト / ダークの 3 モードと保存済みの設定は引き続き利用でき、データ・同期・書き込み処理・Wiki 記法は変更しません。

**English summary**: This release includes all changes since v1.5.0 and updates the entire interface to Pop styling: a warm paper canvas in Light, a black canvas in Dark, yellow primary buttons, crisp outlines, small corners, and hard shadows. System / Light / Dark modes and saved settings remain available. Dense tables use strong outer borders with lighter internal rules; rendered content uses bold headings, thick yellow link underlines, outlined code blocks, and quote rules. Keyboard focus and secondary text remain readable in both modes, selected and disabled controls are visually distinct, and startup backgrounds match the theme. Primary buttons stay stationary on hover when the OS requests reduced motion. Data, sync, write operations, and Wiki notation are unchanged. As an existing limitation, the macOS title bar follows only the OS appearance setting.

## 新機能

- **全画面を Pop スタイルへ統一**: ライトは暖かい紙色、ダークは黒い画布に、黄色の主ボタン・明確な輪郭・小さな角丸・硬い影を組み合わせます。システム / ライト / ダークの 3 モードと保存済みの設定はそのまま利用できます。

## 改善・修正

- **表と整形表示の外観**: 密な表は外枠を強く、内部の罫線を淡くしました。見出しの太字、リンクの黄色い太い下線、コードブロックの輪郭、引用の左罫線を統一しました。データ・同期・書き込み処理・Wiki 記法は変更しません。
- **操作状態と可読性**: 明暗両モードで焦点環と補助文字を見やすくし、選択中や無効な操作部品を視覚的に区別できるようにしました。
- **起動時の背景**: 画面の描画前からテーマに合う背景色を適用します。
- **動きを減らす設定への追従**: OS の動きを減らす設定が有効な場合、主ボタンはホバーしても移動しません。

## 動作環境

- Windows 10/11(x64)/ macOS(Universal)
- Backlog API キー(ユーザ抽出の全件取得には管理者またはプロジェクト管理者権限)

## インストール

zip を展開し、`backlog-assistant.exe`(Windows)または `Backlog Assistant.app`(macOS)を起動してください。詳細は [ユーザガイド](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.md) / [User Guide](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.en.md) を参照してください。

**注意: 現時点では Windows / macOS とも配布バイナリはコード署名されていません。** 必ず本リポジトリの Releases からダウンロードしたものだけを利用してください。

- Windows: 初回起動時に SmartScreen の警告が出る場合があります。「詳細情報」→「実行」で起動できます。
- macOS: 署名・公証未対応のため、初回起動時に Gatekeeper にブロックされます。一度開こうとした後、「システム設定 → プライバシーとセキュリティ」下部の「このまま開く」を選択してください。

## 既知の制限

- macOS のタイトルバーは OS の外観設定のみに従います。アプリ内でライト / ダークを指定しても、タイトルバーだけ OS 側の配色のまま残ることがあります(既存の制限)。
- Wiki の書き込みは単ページ編集・新規作成のみです。一括書き戻し、削除、タグ編集、添付アップロード、自動定期同期には対応していません。
- Backlog 記法と Markdown の相互変換は行いません。プロジェクトの記法設定に従って表示・出力します。
- 保存前の競合確認と実際の保存の間には短い競合窓が残ります(API に楽観ロックがないため)。その間の他者更新を完全には防げません。
- [Backlog の Wiki 提供方針変更](https://backlog.com/ja/blog/backlog-update-wiki-end-of-support-new-spaces/)により、2026-07-14 以降に新規利用を開始したスペースでは Wiki を利用できません。既存スペースの新規プロジェクトでは既定 OFF ですが、プロジェクト設定で有効にできます。
- `#CLEAR#`(担当者・期限・詳細・カスタム属性・親課題のクリア)は公式 API 仕様に明記が無いため実機検証中の機能です
- カスタム属性の「その他」直接入力(otherValue)の書き込みは未対応です
- 課題の親子関係は 2 階層(親 - 子)のみ対応です(Backlog が今後提供予定の孫課題(3 階層)は未対応)
- 一括更新・追加は Backlog のデータを変更します。初回はテスト用プロジェクトでの試行を推奨します
