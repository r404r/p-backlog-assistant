# backlog-assistant

Nulab [Backlog](https://backlog.com/ja/) の課題抽出(Excel 出力)・一括更新・一括追加・ユーザ一覧抽出を行うデスクトップアプリです。

- 技術スタック: [Wails v2](https://wails.io/) + Vue 3 + TypeScript / Go
- ローカルキャッシュ: SQLite([modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)、純 Go)
- Excel 出力: [excelize](https://github.com/qax-os/excelize)
- 配布対象: Windows / macOS

## 状態

マイルストーン 1(接続設定・API クライアント基盤・DB 層)実装済み。課題抽出・同期・一括更新は今後のマイルストーンで実装予定。

## セキュリティ方針

- API キーは **OS キーチェーン**(Windows Credential Manager / macOS Keychain)にのみ保存します。設定ファイル・ログには書き込みません。
- 接続先は HTTPS の `*.backlog.jp` / `*.backlog.com` のみ許可し、リダイレクト追従は無効です。
- 取得データ(ローカル DB)は OS のユーザ設定ディレクトリ配下(0600)に保存し、リポジトリには含まれません。

## 開発

必要環境: Go 1.25+ / Node.js 20+ / Wails CLI v2

```sh
# テスト
go test ./internal/...

# フロントエンドのビルド確認
cd frontend && npm install && npm run build

# 開発モード(要 GUI 環境)
wails dev

# 配布ビルド
wails build
```

macOS 向けビルドは macOS 環境(CI の macos ランナー等)で行います。

## 開発ルール

- TDD(Red → Green → Refactor)で進める
- コミット前に Codex Review を 2 回実施する
- 課題データ・実スペース情報などの実データをリポジトリに含めない
