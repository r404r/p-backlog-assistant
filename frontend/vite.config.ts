// Vite / Vitest の設定。
// テスト設定を別ファイル(vitest.config.ts)に分けると解決条件が二重管理になるため、
// ビルドと同じ設定ファイルへ test セクションを同居させる(defineConfig は vitest/config から取る)。
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue()],
  test: {
    // localStorage / window を参照するモジュール(projectSelection・backend)を
    // そのままテストできるようにブラウザ相当の環境で実行する。
    //
    // jsdom ではなく happy-dom を使う理由: jsdom 30 の engines は Node
    // ^22.22.2 || ^24.15.0 || >=26 で、package.json が宣言する対応 Node
    // (^20.19.0 || ^22.13.0 || >=24.0.0)より狭く、Node 20 系で EBADENGINE に
    // なる。テストが必要とするのは localStorage・window 程度の軽い DOM だけなので、
    // engines が >=20.0.0 の happy-dom で十分足りる(起動も速い)。
    environment: 'happy-dom',
    // Node 26 は実験的 Web Storage を globalThis に公開するが、
    // --localstorage-file 未指定時は undefined になり happy-dom の Storage を
    // 隠してしまう。テスト起動時にブラウザ相当の Storage を明示的に保証する。
    setupFiles: ['src/testing/setup.ts'],
    include: ['src/**/*.test.ts'],
  },
})
