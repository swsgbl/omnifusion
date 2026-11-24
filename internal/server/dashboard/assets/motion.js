// OFD.motion — dashboard 共享微动效层（GSAP）。设计纪律：克制（进场/
// 状态强调，不做炫技）；尊重 prefers-reduced-motion（动效整体禁用，且随
// 系统设置实时切换）；数据轮询只对首帧动效（按容器去重，5s 刷新不闪）。
// 页面经 <script src="assets/gsap.min.js" defer> + 本文件（同样 defer）
// 接入，各页在渲染函数里按需调用；无 GSAP / 减少动效时动效全部为 no-op。
//
// vendorName 是数据不是动效：任何降级路径下都必须在位。providers/keys/
// usage/resilience 四页的 vname() 直接调它——少了它，表格一行都渲染不
// 出来（开启「减少动效」的用户看到的是空表 + TypeError）。
(() => {
  'use strict';

  const noop = () => {};
  // 动效可用性每次调用时现判：GSAP 在位，且用户没有要求减少动效。
  const reduceQuery = window.matchMedia ? matchMedia('(prefers-reduced-motion: reduce)') : null;
  const motionOn = () => typeof gsap !== 'undefined' && !(reduceQuery && reduceQuery.matches);
  if (typeof gsap !== 'undefined') gsap.defaults({ duration: 0.45, ease: 'power2.out' });

  // 只动首帧的容器（轮询重建的行不再是"新内容"，不重播进场）。
  const entered = new WeakSet();
  const grown = new WeakSet();

  // page：页面骨架进场（header + 表头 + 卡片，自上而下轻微上移淡入）。
  const page = () => {
    // 脚本是 defer 进来的：浏览器若已画过首帧，再从透明淡入只会让内容
    // "闪一下"——此时保持静态。页面在后台标签里加载同理。
    if (!motionOn() || document.hidden || performance.getEntriesByType('paint').length) return;
    const targets = [document.querySelector('header'), ...document.querySelectorAll('table thead th'), ...document.querySelectorAll('.card, .meta')]
      .filter(Boolean);
    if (targets.length) {
      gsap.from(targets, { autoAlpha: 0, y: 10, stagger: 0.04, duration: 0.35, clearProps: 'all' });
    }
  };

  // rows：表格数据行首帧进场（同一 tbody 只动一次，轮询刷新不重复）。
  const rows = (tbody) => {
    if (!tbody || entered.has(tbody) || !tbody.children.length || !motionOn() || document.hidden) return;
    entered.add(tbody);
    gsap.from(tbody.children, { autoAlpha: 0, y: 6, stagger: 0.025, duration: 0.28, clearProps: 'all' });
  };

  // bubble：对话气泡进场（新消息上浮淡入）。后台追加的气泡不动——
  // 隐藏页面上动画不推进，从透明起步的气泡会一直不可见。
  const bubble = (el) => {
    if (!el || !motionOn() || document.hidden) return;
    gsap.from(el, { autoAlpha: 0, y: 10, scale: 0.985, duration: 0.28, clearProps: 'all' });
  };

  // bars：用量条首帧从左侧生长到位。按 tbody 去重——轮询每 5s 重建整张
  // 表，按节点去重等于不去重（曾让所有用量条每 5s 重长一遍）。走
  // scaleX（合成器属性），不像 width 那样每帧触发布局。
  const bars = (tbody) => {
    if (!tbody || grown.has(tbody) || !motionOn() || document.hidden) return;
    grown.add(tbody);
    const spans = tbody.querySelectorAll('.bar > span');
    if (spans.length) {
      gsap.from(spans, { scaleX: 0, transformOrigin: 'left center', duration: 0.8, ease: 'power3.out', clearProps: 'scaleX, transformOrigin' });
    }
  };

  // pulse：装饰圆点呼吸（运行中状态的持续微强调；文字本身不动）。
  const pulse = (sel) => {
    if (!motionOn() || document.hidden) return;
    const els = document.querySelectorAll(sel);
    if (els.length) {
      gsap.to(els, { scale: 1.12, duration: 0.9, ease: 'sine.inOut', repeat: -1, yoyo: true, transformOrigin: '50% 50%' });
    }
  };

  // 厂商双语名：[中文名, English name]。zh 模式 → "中 (En)"，en 模式 → "En (中)"；
  // 同名只显示一次。新厂商在此登记。
  const VENDORS = {
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
  const vendorName = (id, lang) => {
    const v = VENDORS[id];
    if (!v) return id || '';
    if (v[0] === v[1]) return v[0];
    return lang === 'zh' ? `${v[0]} (${v[1]})` : `${v[1]} (${v[0]})`;
  };

  // 合并而非替换：OFD 命名空间可能已有其他模块。
  Object.assign((window.OFD = window.OFD || {}), { motion: { page, rows, bubble, bars, pulse, vendorName } });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', page, { once: true });
  } else {
    page();
  }
})();
