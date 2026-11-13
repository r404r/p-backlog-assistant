import {expect,it} from 'vitest'
import {readFileSync} from 'node:fs'
it('keeps two logical rows and puts operation status below both',()=>{
 const source=readFileSync('src/views/WikiView.vue','utf8')
 expect(source).toContain('wiki-header-row--project')
 expect(source).toContain('wiki-header-row--export')
 expect(source.indexOf('class="wiki-operation-status"')).toBeGreaterThan(source.indexOf('class="wiki-header-row--export"'))
 expect(source).toContain('headerWidth < 900')
 expect(source).toContain('headerWidth < 520')
})
