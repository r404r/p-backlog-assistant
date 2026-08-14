import {createApp} from 'vue'
import App from './App.vue'
import {initTheme} from './lib/theme'
import './style.css';

// テーマの初期化はマウント前に 1 回だけ行う(初期化の所有者は main.ts)。
// prepaint.js が付けた data-theme をここで解決し直して上書きする。
initTheme()

createApp(App).mount('#app')
