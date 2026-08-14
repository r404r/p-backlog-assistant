import {createApp} from 'vue'
import App from './App.vue'
import {i18n} from './lib/i18n'
import {initLanguage} from './lib/language'
import {initTheme} from './lib/theme'
import './style.css';

// テーマの初期化はマウント前に 1 回だけ行う(初期化の所有者は main.ts)。
// prepaint.js が付けた data-theme をここで解決し直して上書きする。
initTheme()

// 表示言語もマウント前に解決しておく(初期化の所有者は main.ts)。
// i18n インスタンスの locale と <html lang> がここで確定するため、
// 最初の描画から正しい言語で表示される。
initLanguage()

createApp(App).use(i18n).mount('#app')
