/*
 * 起動時のちらつき(FOUC)対策の第 1 層。
 *
 * index.html の <head> でブロッキング読込され、アプリ本体(main.ts)より前に
 * 同期実行される。保存済みのテーマ設定を localStorage から読み、
 * <html> に data-theme と style.colorScheme を付けてから最初の描画を迎えることで、
 * ダーク設定なのに一瞬白い画面が出る現象を防ぐ。
 *
 * 注意:
 *  - ES モジュールではなくクラシックスクリプト(同期実行)であること。
 *    defer / async / type="module" を付けると最初の描画に間に合わない。
 *  - 処理全体を try/catch で包み、**どの例外経路でもライトとして必ず属性を設定する**。
 *    属性が付かないまま進むと配色が確定せず、アプリ起動までの間だけ地の色が出る。
 *    ここで多少取り違えても、起動直後に theme.ts が解決し直して上書きする。
 *  - localStorage キー・モードの解釈は src/lib/theme.ts と揃えること
 *    (乖離は src/lib/theme.test.ts がこのファイルを実行して検査する)。
 */
(function () {
  // 既定はライト。判定できた場合だけダークへ倒す。
  var theme = 'light';

  try {
    // localStorage の参照自体が例外になる WebView 設定では、外側の catch でライトのままにする
    var mode = window.localStorage.getItem('ba.themeMode');
    // 'system'・未保存・不正値はすべて OS 追従として扱う
    if (mode !== 'light' && mode !== 'dark') {
      mode = 'system';
    }

    if (mode === 'system') {
      // matchMedia は不存在(古い WebView)も、呼び出し例外もあり得る。
      // 不存在ならライトのまま、例外は外側の catch がライトとして拾う。
      if (typeof window.matchMedia === 'function') {
        if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
          theme = 'dark';
        }
      }
    } else {
      theme = mode;
    }
  } catch {
    theme = 'light';
  }

  try {
    var root = document.documentElement;
    root.setAttribute('data-theme', theme);
    root.style.colorScheme = theme;
  } catch {
    // ここまで失敗する場合は打つ手が無い(アプリ起動後に theme.ts が再試行する)
  }
})();
