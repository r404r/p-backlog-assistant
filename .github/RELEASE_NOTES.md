# backlog-assistant v1.2.1

データの保存先を変更できるようにするリリースです。

**English summary**: You can now customize where the app stores its data (config and local DB). Put a `portable.txt` file next to the executable (next to the `.app` bundle on macOS) to keep the config and local DB in a `userdata` folder alongside the app, or set the `BACKLOG_ASSISTANT_HOME` environment variable to an absolute path (no `~`/`%VAR%` expansion, paths containing `?` are rejected). Portable mode keeps the config and local DB alongside the app — handy for USB drives or moving between PCs, though API keys stay in the OS keychain and must be re-entered on a new PC, and switching locations never migrates existing data automatically. If an explicitly chosen location becomes unavailable, the app stops at startup instead of silently creating a new data set elsewhere. See the [User Guide](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.en.md) for details.

## 新機能

- **データ保存先のカスタマイズ**: 設定(config.json)とローカル DB の保存先を 2 つの方法で変更できるようになりました(優先順位: ポータブル > 環境変数 > 既定)
  - **ポータブルモード**: 実行ファイル(macOS は `.app` バンドル)の**隣**に `portable.txt` を置くと、同じ場所の `userdata` フォルダにデータを保存します。フォルダごとコピーするだけで別のパソコンへ移行できます(API キーのみ再入力が必要です)
  - **環境変数**: `BACKLOG_ASSISTANT_HOME` に絶対パスを設定すると、そのフォルダを保存先にします(`~` や `%VAR%` は展開されません。`?` を含むパスは使用できません)
  - 指定した保存先が使えない場合は、別の場所へ黙ってデータを新規作成せず**起動を中止**してお知らせします(保存先の分断防止)
  - 保存先を切り替えてもデータは自動移行されません。継続利用するには、アプリ終了後に保存先フォルダ全体をコピーしてください
  - 現在の保存先とモードは「アプリ情報」画面で確認できます。詳細は [ユーザガイド](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.md) の「保存先の変更」を参照してください

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
