# backlog-assistant v1.7.0

Wiki の読みやすさと操作性を高めるリリースです。v1.6.0 以降の変更と依存ライブラリの更新を含みます。コードの構文ハイライト・読み取り専用タスク・CJK 強調・排版は、Wiki に加えて課題詳細とコメントにも適用されます。

**English summary**: This release includes all changes since v1.6.0. Wiki, issue details, and comments gain syntax highlighting with language labels and copy buttons, read-only task lists, CJK emphasis, and improved typography. Wiki also gains a heading-based table of contents, inline images from the current page’s attachments, and exact-match links to pages in the same project. Markdown parsing and sanitization dependencies are updated to fix slow rendering for certain content. Supported attachment formats are PNG, JPEG, and GIF (first frame only), limited to 8 MiB, 16 million pixels, and 8192 pixels per side. External images and raw HTML are not rendered, Backlog-format pages remain plain text, and pages over 2 MiB show source by default.

## 新機能

- **コードの構文ハイライトとコピー**: 12 言語のコードブロックに構文ハイライト・言語ラベル・コピー操作を追加しました。未対応言語はコード原文を表示します。
- **読み取り専用タスクリスト**: 未完了・完了のタスクを見分けやすく表示します。表示上のチェックは変更できません。
- **Wiki の目次**: 右側の折りたたみ目次から見出しへ移動できます。現在位置の表示とキーボード操作に対応します。
- **Wiki 添付画像**: 表示中の Wiki 自身に添付された画像を本文内に表示します。読み込みに失敗した場合も原文と理由を残します。
- **Wiki 内部リンク**: `[[ページ名]]` から同じプロジェクトのページへ移動できます。見つからない場合や同名ページが複数ある場合は理由を表示します。

## 改善・修正

- **CJK 強調と排版**: 日本語などの句読点に隣接する強調に対応し、本文幅・見出し・引用・区切り線・表の表頭と縞模様を Pop スタイルで整えました。幅の広い表とコードは横スクロールできます。従来の単改行と旧テンプレート表の表示も維持します。
- **依存ライブラリの更新**: Markdown の解析・サニタイズに関わる依存を更新し、特定の内容で表示が遅くなる問題を修正しました。DOMPurify 3.4.17、markdown-it 15.0.3、Vue と関連コンパイラ 3.5.45、vue-i18n と `@intlify/*` 11.4.18、source-map-js 1.2.2 を使用します。

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
