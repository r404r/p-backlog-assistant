/// <reference types="vite/client" />

// .vue を import 可能にするためのフォールバック宣言(Vite の雛形由来)。
// vue-tsc で型検査する際は各 SFC の実際の型が優先されるため、この宣言は
// 素の tsc / エディタが SFC を解決できない場合の保険として残している。
// 具体的な props / データ型は宣言できないため object・unknown で受ける。
declare module '*.vue' {
    import type {DefineComponent} from 'vue'
    const component: DefineComponent<object, object, unknown>
    export default component
}
