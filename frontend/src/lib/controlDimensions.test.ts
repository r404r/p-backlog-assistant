import { expect,it } from 'vitest'
import {readFileSync} from 'node:fs'
it('defines identical ordinary control dimensions in both themes',()=>{
 const css=readFileSync('src/style.css','utf8')
 expect(css.match(/--control-height:\s*2.25rem/g)).toHaveLength(2)
 expect(css).toContain('min-block-size: var(--control-height)')
 expect(css).toContain('line-height: var(--control-line-height)')
 for(const view of ['IssuesView','BulkUpdateView','UsersView','SyncStatusView','SettingsView']){
  const source=readFileSync(`src/views/${view}.vue`,'utf8')
  expect(source).not.toMatch(/select\s*\{\s*padding: 0.4rem 0.5rem/)
 }
})
