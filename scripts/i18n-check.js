// i18n 一致性校验：桌面壳的「空白菜单/漏字」回归护栏。
// 用法：node scripts/i18n-check.js [apps/desktop/ui/index.html]
//
// 校验四件事：
//   1. zh / en 顶层键集合完全一致（单边改文案必被发现）；
//   2. 嵌套表 detail / tabs 的键集合也一致；
//   3. 代码里出现的每个 t.<key> 都真实存在于字典（防 undefined 渲染成空白）；
//   4. 侧边栏每个页面都有对应的 tabs 文案。
const fs = require("fs");

const file = process.argv[2] || "apps/desktop/ui/index.html";
const src = fs.readFileSync(file, "utf8");

function extractLiteral(text, decl) {
  const start = text.indexOf(decl);
  if (start < 0) throw new Error("未找到 " + decl);
  const open = text.indexOf("{", start);
  let depth = 0;
  for (let p = open; p < text.length; p++) {
    if (text[p] === "{") depth++;
    else if (text[p] === "}") {
      depth--;
      if (depth === 0) return text.slice(open, p + 1);
    }
  }
  throw new Error("字面量未闭合: " + decl);
}

// eslint-disable-next-line no-eval
const I18N = eval("(" + extractLiteral(src, "const I18N = {") + ")");
const arrayDecl = (name) => {
  const m = src.match(new RegExp("const " + name + " = (\\[[^\\]]*\\])"));
  if (!m) throw new Error("未找到 " + name);
  // eslint-disable-next-line no-eval
  return eval("(" + m[1] + ")");
};
const PAGES = arrayDecl("DASH_PAGES").concat(arrayDecl("SHELL_PAGES"));

const errs = [];
const keys = (o) => Object.keys(o).sort();
const diff = (a, b) => a.filter((k) => !b.includes(k));

for (const lang of keys(I18N)) {
  diffs(I18N.zh, I18N[lang], "顶层");
  for (const nested of ["detail", "tabs"]) {
    if (I18N.zh[nested] && I18N[lang][nested]) diffs(I18N.zh[nested], I18N[lang][nested], nested);
  }
}
function diffs(a, b, where) {
  const onlyA = diff(keys(a), keys(b));
  const onlyB = diff(keys(b), keys(a));
  for (const k of onlyA) errs.push(`[${where}] zh 有而其它语言缺: ${k}`);
  for (const k of onlyB) errs.push(`[${where}] 其它语言有而 zh 缺: ${k}`);
}

// 引用检查：t.foo / t.detail[..] 里的 foo 必须是字典键。
const refs = new Set();
for (const m of src.matchAll(/\bt\.([A-Za-z_][A-Za-z0-9_]*)/g)) refs.add(m[1]);
for (const r of [...refs].sort()) if (!I18N.zh[r]) errs.push(`代码引用了字典里没有的键: t.${r}`);

// 页面覆盖：每个可打开页面都要有 tabs 文案，否则侧边栏/标签页标题空白。
const tabs = I18N.zh.tabs ? Object.keys(I18N.zh.tabs) : [];
for (const p of PAGES) if (!tabs.includes(p)) errs.push(`页面缺少 tabs 文案: ${p}`);

if (errs.length) {
  console.error("i18n 校验失败（" + errs.length + " 项）:");
  for (const e of errs) console.error("  - " + e);
  process.exit(1);
}
console.log(`i18n OK：${keys(I18N).length} 语言 × ${keys(I18N.zh).length} 键，${PAGES.length} 个页面文案齐全`);
