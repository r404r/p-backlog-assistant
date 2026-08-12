// ESLint(flat config)の設定(R15)。
//
// 方針: 「壊れる書き方」を検出するためのものであり、整形(インデント・改行位置)は
// 対象にしない。整形ツール(Prettier 等)は導入していないため、整形系ルールを有効に
// すると既存の 8,000 行超のテンプレートを一斉に書き換えることになり、
// レビュー可能な差分にならないためである。
import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import { defineConfigWithVueTs, vueTsConfigs } from '@vue/eslint-config-typescript'
import globals from 'globals'

export default defineConfigWithVueTs(
  {
    // 生成物・ビルド成果物は検査しない。
    // wailsjs/ は Wails CLI が生成するバインディングで、手で直さないため除外する。
    ignores: ['dist/**', 'wailsjs/**', 'node_modules/**'],
  },
  js.configs.recommended,
  pluginVue.configs['flat/recommended'],
  vueTsConfigs.recommended,
  {
    files: ['**/*.{ts,vue,js}'],
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      // WebView 上で動くフロントエンドのため、ブラウザのグローバル(window,
      // localStorage, console 等)を既知とする
      globals: globals.browser,
    },
    rules: {
      // 未使用の変数はエラー。ただし先頭 _ の変数・引数は「意図的に使わない」印として許可する
      // (シグネチャを満たすためだけの引数、分割代入での除外など)。
      // catch の例外オブジェクトも同じ規約で検査する(握りつぶしを見落とさないため)。
      '@typescript-eslint/no-unused-vars': [
        'error',
        {
          argsIgnorePattern: '^_',
          varsIgnorePattern: '^_',
          caughtErrors: 'all',
          caughtErrorsIgnorePattern: '^_',
        },
      ],

      // --- 以下は整形系のため無効化(上記の方針を参照)。括弧内は無効化時点の指摘件数 ---
      // 単一行要素の内容を改行させるルール(290 件)
      'vue/singleline-html-element-content-newline': 'off',
      // 属性を 1 行 1 つに折り返すルール(317 件)
      'vue/max-attributes-per-line': 'off',
      // 空要素の自己終了タグを強制するルール(42 件)
      'vue/html-self-closing': 'off',
    },
  },
)
