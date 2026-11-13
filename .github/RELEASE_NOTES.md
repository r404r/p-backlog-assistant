# backlog-assistant v1.8.0

Wiki ページツリーの操作性と、小さいウィンドウでの使いやすさを改善するリリースです。v1.7.0 以降の変更を含みます。ページツリーの表示順は、従来のページ ID 順から名前の Unicode コードポイント順に変わります。

**English summary**: This release includes all changes since v1.7.0. The Wiki tree gains folding, Expand all / Collapse all, distinct directory and page icons, and descendant page counts. The current page and the hierarchy selected for export or new pages have separate indicators; selecting a pure directory keeps the body unchanged. Keyboard support includes Up/Down, Left/Right, Home/End, Enter/Space, and typing a name prefix. Tree display changes from page ID order to unnormalized Unicode code point order: `10` precedes `2`, and uppercase precedes lowercase. The body, contents, and tree scroll separately when space permits. At low heights, the panel stays on the right and scrolls as a whole; below 760 CSS px of actual Wiki layout width, it becomes an initially closed drawer opening from the right. Close it with Close, Escape (except during IME composition), or the backdrop; successfully opening a page or choosing a contents entry also closes it. In both compact modes, contents remain independently scrollable, capped at 25% of visible panel height (up to 192px). Data, synchronization, export content and order, and Wiki write behavior remain unchanged.

## 新機能

- **ページツリーの折りたたみ**: 下位のある行の矢印で展開・折りたたみができます。既定はすべて展開で、「すべて展開」「すべて折りたたむ」は検索で非表示の枝にも適用します。ページ切替・検索・一覧更新・パネルの開閉では状態を維持し、プロジェクト切替・画面への再入場では全展開に戻ります。
- **ディレクトリとページの区別**: ページを持たないディレクトリはフォルダアイコンと太字、ページは文書アイコン、下位を持つページは文書アイコンと矢印で表示します。ディレクトリと下位を持つページには下位の全ページ数を表示します。検索中の数は表示範囲内のページ数で、出力件数ではありません。
- **現在ページと選択階層の表示**: 表示中のページは背景色と左線、出力・新規作成の対象階層は破線とチェックで区別します。ディレクトリを選んでも本文は変わらず、矢印は枝の開閉だけを行います。
- **キーボード操作**: 上下で行移動、右で展開・最初の子へ移動、左で折りたたみ・親へ移動、Home / End で先頭・末尾へ移動、Enter / Space でページや階層を選択できます。文字入力で名前の先頭が一致する行へ移動します。焦点を動かすだけでは本文や出力対象は変わりません。

## 改善・修正

- **名前順への変更**: ページツリーを、v1.7.0 のページ ID 順から名前の Unicode コードポイント順へ変更しました。名前の正規化は行わず、`10` は `2` より前、英字の大文字は小文字より前に並びます。ディレクトリとページを混在して並べ、同名ページは ID 付きの別行で表示します。この変更はツリー表示だけに適用します。
- **独立スクロール**: 高さに余裕がある時は検索を固定し、本文・目次・ページツリーをそれぞれスクロールできます。ツリーの下の方からページを開いても、本文だけが先頭に戻り、すでに見えている行の位置は維持します。検索結果や内部リンクから深いページを開くと、その祖先だけを展開して行を見える位置へ移します。
- **小さいウィンドウの配置**: 高さが足りない時もパネルは本文の右側に残り、検索とページツリーを含むパネル全体をスクロールできます。Wiki 領域の実際の幅が 760 CSS px 未満になると、右側から開くドロワーに変わり、初めは閉じています。「検索・ツリーを表示」で開き、「閉じる」、Esc（日本語変換中を除く）、背景のオーバーレイのクリックで閉じます。ページを正常に開いた時や目次の項目を選んだ時も閉じます。どちらの状態でも目次は可視パネル高の 25% 以下（最大 192px）に収まり、超えた項目は目次内でスクロールでき、直後にページツリーが続きます。パネルが本文の下に移ることはありません。
- **既存データと処理の維持**: データ、同期、エクスポートの内容と順序、Wiki の書き込み処理は変更していません。

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
