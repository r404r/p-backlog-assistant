import { describe, expect, it } from 'vitest'
import { highlightCode } from './markdownHighlight'
import { codeLanguage, prepareMarkdownDocument } from './markdownDocument'
const samples:Record<string,string>={javascript:'const x = "hello";',typescript:'const x: number = 42;',json:'{"x":true}',bash:'echo "hello"',go:'func main() { println("hello") }',python:'print("hello")',xml:'<div class="x">hello</div>',css:'.x { color: red; }',sql:'SELECT * FROM samples;',yaml:'active: true',markdown:'# Hello',diff:'+ added\n- removed'}
describe('固定言語のWorker高亮',()=>{
 for(const [language,source] of Object.entries(samples)) it(language,()=>{
  const doc=prepareMarkdownDocument(`\`\`\`${language}\n${source}\n\`\`\``)
  const host=document.createElement('div');host.innerHTML=doc.render({highlights:new Map([[0,highlightCode(source+'\n',language)]])})
  expect(host.querySelector('code')?.textContent).toBe(source+'\n')
  expect(host.querySelector('code span')).not.toBeNull()
  expect(host.querySelector('code span[class^="md-syntax-"]')).not.toBeNull()
  expect(host.querySelector('img,script,[src],[style]')).toBeNull()
 })
 it.each([['js','javascript'],['ts','typescript'],['sh','bash'],['shell','bash'],['py','python'],['html','xml'],['yml','yaml'],['md','markdown'],['unknown','text'],['text','text'],['','text']])('alias %s', (alias,want)=>expect(codeLanguage(alias)).toBe(want))
})
