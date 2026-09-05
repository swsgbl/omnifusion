// syntax-check.mjs 提取 chat.html 的内联 <script> 做语法检查
//（node --check 不支持从 stdin？支持 --check 文件；这里用 new Function 编译）。
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';

const html = readFileSync(process.argv[2], 'utf8');
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m => m[1]);
if (!scripts.length) { console.error('no inline scripts found'); process.exit(1); }
const dir = mkdtempSync(join(tmpdir(), 'ofd-js-'));
scripts.forEach((s, i) => writeFileSync(join(dir, `s${i}.js`), s));
scripts.forEach((_, i) => {
  try {
    execFileSync(process.execPath, ['--check', join(dir, `s${i}.js`)], { stdio: 'pipe' });
    console.log(`script ${i}: syntax OK (${scripts[i].length} chars)`);
  } catch (e) {
    console.error(`script ${i}: SYNTAX ERROR\n` + e.stderr);
    process.exit(1);
  }
});
// 关键符号在位检查：符号列表可由后续命令行参数覆盖（默认为 chat 页
// 四手清单；keys 等其他页面传自己的符号）。
const all = scripts.join('\n');
const symbols = process.argv.length > 3 ? process.argv.slice(3) :
  ['read_file', 'edit_file', 'write_file', 'run_command', 'askApproval', 'approval_required', 'butler/run-command', 'butler/edit-file'];
for (const sym of symbols) {
  if (!all.includes(sym)) { console.error('MISSING symbol: ' + sym); process.exit(1); }
}
console.log('all requested symbols present');
