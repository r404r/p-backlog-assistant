# backlog-assistant v1.9.0

Wiki 添付ファイルの個別ダウンロードを追加し、本文の枠と画面上部の配置、アプリ全体の操作コントロールを改善するリリースです。v1.8.0 以降の変更を含みます。

**English summary**: This release includes all changes since v1.8.0. Wiki attachments can be downloaded individually from a counted list at the end of the body; the section is hidden when there are no attachments. Each download opens the system save dialog with a sanitized default filename, shows progress, and supports cancellation. Inline images are saved as original files, including animated GIFs, and never opened automatically. Only one attachment can be downloaded at a time across the app; starting sync, export or editing is disabled during a download. Changing pages, projects, screens or connection settings cancels the download without an extra confirmation and waits for cleanup; the existing unsaved-change confirmation still applies. Normal quit also cancels and waits. Cancellation, interruption or a mismatch with the cached size leaves an existing destination file unchanged and does not save an incomplete file. Size mismatches ask you to refresh the page; failures have distinct guidance. Cleanup failures are reported, and force quit, crashes or power loss may leave a temporary file with no automatic recovery. On POSIX systems, saved files have owner-only read/write permissions (0600), including replacements. Ordinary buttons, single-select dropdowns and single-line inputs now share a height across all screens; bulk job history rows are taller, and their action column uses horizontal table scrolling. The Wiki header changes from three rows to two, with export progress and cancellation below them only while processing, a fixed-width project selector with ellipsis and hover text, and grouped wrapping in narrow windows. Older WKWebView versions without `:where()` retain previous control heights. Wiki content now has a bordered frame, and excess whole-screen scrolling after reaching the body end is fixed at normal text sizes. At 200% text size, an expanded navigation sidebar can still cause extra vertical scrolling; at 320px window width and 200%, the header can overflow horizontally. Excel export failures retain the underlying reason. Wiki export now also sanitizes reserved names containing superscript digits, such as `COM¹`. The database schema, synchronization data and Wiki write payloads are unchanged.

## 新機能

- **添付ファイルの個別ダウンロード**: 添付があるページでは本文末尾に件数付きの一覧を表示し、各行の「ダウンロード」から保存できます。添付がなければ一覧全体を表示しません。毎回システムの保存ダイアログを開き、取得済みの添付名を安全なファイル名に変換して既定名にします。保存先の選択を取り消すと通信を開始しません。取得中は進捗と「キャンセル」を表示します。
- **原本を保存**: 本文内に表示した画像も対象で、GIF を含め元のファイルをそのまま保存します。保存後に自動で開いたり実行したりしません。ページ全体のバックアップには従来の添付込みエクスポートを利用できます。
- **キャンセルと失敗時の保護**: キャンセル、通信中断、一覧の記録とのサイズ不一致ではファイルを保存せず、既存の同名ファイルを変更しません。サイズが一致しない場合は「最新の状態を取得」後に再試行するよう案内します。添付の消失、認証、権限、通信、保存先の問題は原因ごとに案内し、一時ファイルの削除に失敗した場合も成功とは表示しません。
- **同時実行と画面移動**: アプリ全体で同時にダウンロードできるのは一件です。取得中は他の添付や同期・出力・編集の開始を無効にします。別ページ、プロジェクト、画面、接続設定への移動では、追加の確認を出さずにダウンロードをキャンセルし、一時ファイルの削除を待ってから移動します。既存の未保存編集の確認は引き続き行います。通常終了時もキャンセルと後片付けを待ちます。移動によるキャンセルは元のページに戻ると「キャンセルしました」と表示します。
- **保存時の注意**: 強制終了、クラッシュ、停電では保存先に `.wiki-attachment-*.tmp` が残ることがあり、起動時の自動回収は行いません。macOS などの POSIX 環境では保存ファイルの権限は 0600（所有者のみ読み書き可能）で、既存の 0644 ファイルを上書きした場合も 0600 になります。

## 改善・修正

- **全画面のコントロールの高さ**: 通常のボタン、単一選択のドロップダウン、1 行の入力欄を同じ高さに統一しました。一括更新の履歴表も行が高くなり、操作列は折り返さず表の横スクロールで操作できます。アイコンボタン、日付、複数行入力などは従来の寸法を維持します。`:where()` に対応しない旧 WKWebView では従来の高さのままです。
- **Wiki 上部の二行配置**: 従来の三行から二行へ変更し、プロジェクト・同期・最終同期時刻・検索パネルの開閉を一行目、新規ページ・エクスポート・「同期してから出力」を二行目にまとめました。出力の進捗とキャンセルは二行の下へ移し、処理中だけ表示します。プロジェクト選択は固定幅とし、長い名前は省略して、ポインターを重ねると全名を表示します。狭いウィンドウではグループごとに折り返します。
- **Wiki 本文の枠とスクロール**: ライト / ダークに対応した輪郭と余白のある枠内に、ページ名・本文・添付一覧を表示します。整形表示、原文、編集でも同じ枠を使い、本文・目次・ツリーの独立スクロールを維持します。通常の文字サイズで本文末尾の後に画面全体が余分にスクロールする問題を修正しました。
- **拡大表示時の制限**: 文字サイズ 200%・左ナビゲーション展開時には、Wiki 画面の外側に縦スクロールが増える場合があります。ウィンドウ幅 320px・文字サイズ 200% では、Wiki 上部が横にはみ出す場合があります。
- **Excel 出力のエラー案内**: 保存先への書き込みや置換に失敗した時、原因が分かるエラー文を保持するよう修正しました。
- **Wiki 出力のファイル名**: 添付保存と共通の名前処理を使い、上付き数字を含む Windows の予約名（`COM¹` など）にも下線を付けます。データベースの構造、同期するデータ、Wiki の書き込み内容は変更していません。ダウンロード中の処理制限と、出力名・エラー案内の変更は上記のとおりです。

## 動作環境

- Windows 10/11(x64)/ macOS(Universal)
- Backlog API キー(ユーザ抽出の全件取得には管理者またはプロジェクト管理者権限)

## インストール

zip を展開し、`backlog-assistant.exe`(Windows)または `Backlog Assistant.app`(macOS)を起動してください。詳細は [ユーザガイド](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.md) / [User Guide](https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.en.md) を参照してください。

**注意: 現時点では Windows / macOS とも配布バイナリはコード署名されていません。** 必ず本リポジトリの Releases からダウンロードしたものだけを利用してください。

- Windows: 初回起動時に SmartScreen の警告が出る場合があります。「詳細情報」→「実行」で起動できます。
- macOS: 署名・公証未対応のため、初回起動時に Gatekeeper にブロックされます。一度開こうとした後、「システム設定 → プライバシーとセキュリティ」下部の「このまま開く」を選択してください。

## 既知の制限

- Wiki 添付画像は PNG / JPEG / GIF のみ対応し、GIF は先頭フレームだけを表示します。入力・変換後とも 1 枚 8 MiB、1600 万画素、一辺 8192px までです。
- 外部画像と生 HTML は描画しません。Backlog 記法（非 Markdown）のページは引き続き原文のプレーンテキストで表示し、記法変換は行いません。
- 2 MiB を超えるページは既定で原文を表示します。Wiki では明示的に整形表示を選べますが、画像とコードの構文ハイライトは無効になります。
- Wiki 内部リンクは同じプロジェクト内のページ名の完全一致だけに対応します。課題詳細・コメント・編集プレビューでは Wiki 添付画像と内部リンクを解決しません。
- macOS のタイトルバーは OS の外観設定のみに従います。アプリ内でライト / ダークを指定しても、タイトルバーだけ OS 側の配色のまま残ることがあります(既存の制限)。
- Wiki の書き込みは単ページ編集・新規作成のみです。一括書き戻し、削除、タグ編集、添付アップロード、自動定期同期には対応していません。
- Backlog 記法と Markdown の相互変換は行いません。プロジェクトの記法設定に従って表示・出力します。
- 保存前の競合確認と実際の保存の間には短い競合窓が残ります(API に楽観ロックがないため)。その間の他者更新を完全には防げません。
- [Backlog の Wiki 提供方針変更](https://backlog.com/ja/blog/backlog-update-wiki-end-of-support-new-spaces/)により、2026-07-14 以降に新規利用を開始したスペースでは Wiki を利用できません。既存スペースの新規プロジェクトでは既定 OFF ですが、プロジェクト設定で有効にできます。
- `#CLEAR#`(担当者・期限・詳細・カスタム属性・親課題のクリア)は公式 API 仕様に明記が無いため実機検証中の機能です
- カスタム属性の「その他」直接入力(otherValue)の書き込みは未対応です
- 課題の親子関係は 2 階層(親 - 子)のみ対応です(Backlog が今後提供予定の孫課題(3 階層)は未対応)
- 一括更新・追加は Backlog のデータを変更します。初回はテスト用プロジェクトでの試行を推奨します
