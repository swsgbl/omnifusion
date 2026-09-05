// OFD.motion — dashboard 共享微动效层（GSAP）。设计纪律：克制（进场/
// 状态强调，不做炫技）；尊重 prefers-reduced-motion（整体禁用）；数据
// 轮询只对首帧动效（WeakSet 去重，5s 刷新不闪）。页面经
// <script src="assets/gsap.min.js"></script> + 本文件接入，各页在渲染
// 函数里按需调用；无 GSAP / 降级环境下全部为 no-op。
(function () {
  'use strict';
  var noop = function () {};
  var api = { page: noop, rows: noop, bubble: noop, bar: noop, pulse: noop, dot: noop };

  if (typeof gsap === 'undefined') { window.OFD = { motion: api }; return; }
  if (window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches) {
    window.OFD = { motion: api };
    return;
  }

  gsap.defaults({ duration: 0.45, ease: 'power2.out' });
  var seen = new WeakSet();

  // page：页面骨架进场（header + 表头 + 卡片，自上而下轻微上移淡入）。
  api.page = function () {
    var targets = [];
    var header = document.querySelector('header');
    if (header) targets.push(header);
    var rows = document.querySelectorAll('table thead th');
    if (rows.length) targets.push.apply(targets, Array.prototype.slice.call(rows));
    var cards = document.querySelectorAll('.card, .meta');
    if (cards.length) targets.push.apply(targets, Array.prototype.slice.call(cards));
    if (!targets.length) return;
    gsap.from(targets, { autoAlpha: 0, y: 10, stagger: 0.04, duration: 0.35, clearProps: 'all' });
  };

  // rows：表格数据行首帧进场（同一 tbody 只动一次，轮询刷新不重复）。
  api.rows = function (tbody) {
    if (!tbody || seen.has(tbody) || !tbody.children.length) return;
    seen.add(tbody);
    gsap.from(tbody.children, { autoAlpha: 0, y: 6, stagger: 0.025, duration: 0.28, clearProps: 'all' });
  };

  // bubble：对话气泡进场（新消息上浮淡入）。
  api.bubble = function (el) {
    if (!el) return;
    gsap.from(el, { autoAlpha: 0, y: 10, scale: 0.985, duration: 0.28, clearProps: 'all' });
  };

  // bar：用量条从 0 生长到 pct%（同一条只动一次）。
  api.bar = function (span, pct) {
    if (!span || seen.has(span)) return;
    seen.add(span);
    gsap.fromTo(span, { width: '0%' }, { width: Math.max(0, Math.min(100, pct)) + '%', duration: 0.8, ease: 'power3.out' });
  };

  // pulse：状态徽标呼吸（运行中/托管中的持续微强调）。
  api.pulse = function (sel) {
    var els = document.querySelectorAll(sel);
    if (!els.length) return;
    gsap.to(els, { scale: 1.12, duration: 0.9, ease: 'sine.inOut', repeat: -1, yoyo: true, transformOrigin: '50% 50%' });
  };

  // dot：对话"生成中"指示点的交替跳动。
  api.dot = function (el) {
    if (!el) return;
    gsap.to(el.children, { y: -3, duration: 0.35, ease: 'sine.inOut', repeat: -1, yoyo: true, stagger: 0.12 });
  };

  // 厂商双语名：[中文名, English name]。zh 模式 → "中 (En)"，en 模式 → "En (中)"；
  // 同名只显示一次。新厂商在此登记。
  var VENDORS = {
    anthropic: ['Anthropic', 'Anthropic'],
    ark: ['火山方舟（豆包）', 'Volcengine Ark (Doubao)'],
    cerebras: ['Cerebras', 'Cerebras'],
    chutes: ['Chutes', 'Chutes'],
    cloudflare: ['Cloudflare Workers AI', 'Cloudflare Workers AI'],
    cohere: ['Cohere', 'Cohere'],
    deepseek: ['深度求索 DeepSeek', 'DeepSeek'],
    gemini: ['谷歌 Gemini', 'Google Gemini'],
    groq: ['Groq', 'Groq'],
    huggingface: ['HuggingFace', 'HuggingFace'],
    hunyuan: ['腾讯混元', 'Tencent Hunyuan'],
    mimo: ['小米 MiMo', 'Xiaomi MiMo'],
    mistral: ['Mistral', 'Mistral'],
    modelscope: ['魔搭 ModelScope', 'ModelScope'],
    nvidia: ['英伟达 NVIDIA NIM', 'NVIDIA NIM'],
    ollama: ['Ollama（本地）', 'Ollama (local)'],
    openrouter: ['OpenRouter', 'OpenRouter'],
    qianfan: ['百度千帆', 'Baidu Qianfan'],
    qwen: ['通义千问 Qwen', 'Qwen (Alibaba)'],
    sambanova: ['SambaNova', 'SambaNova'],
    siliconflow: ['硅基流动 SiliconFlow', 'SiliconFlow'],
    spark: ['讯飞星火', 'iFlytek Spark'],
    together: ['Together AI', 'Together AI'],
    zhipu: ['智谱 BigModel', 'Zhipu BigModel'],
  };
  api.vendorName = function (id, lang) {
    var v = VENDORS[id];
    if (!v) return id || '';
    if (v[0] === v[1]) return v[0];
    return lang === 'zh' ? v[0] + ' (' + v[1] + ')' : v[1] + ' (' + v[0] + ')';
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { api.page(); });
  } else {
    api.page();
  }
  window.OFD = { motion: api };
})();
