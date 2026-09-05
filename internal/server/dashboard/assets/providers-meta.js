// OFD.pname — 提供商 id → 双语显示名（各表的用户可读渲染）。
// 规则（用户裁定）：中文模式「中文名 (id)」，英文模式「English name (id)」
// ——括号里始终保留 CLI 用的 id（ofd key add zhipu 要对得上号）；
// 显示名与 id 相同时（Groq/DeepSeek 等国际通名）不重复括注。
// 本文件与注册表 YAML 同步维护（新增内置提供商须在此登记）。
(function () {
  'use strict';
  var META = {
    anthropic:   { zh: 'Anthropic',       en: 'Anthropic' },
    ark:         { zh: '火山方舟·豆包',    en: 'Volcengine Ark (Doubao)' },
    cerebras:    { zh: 'Cerebras',        en: 'Cerebras' },
    chutes:      { zh: 'Chutes',          en: 'Chutes' },
    cloudflare:  { zh: 'Cloudflare Workers AI', en: 'Cloudflare Workers AI' },
    cohere:      { zh: 'Cohere',          en: 'Cohere' },
    deepseek:    { zh: 'DeepSeek',        en: 'DeepSeek' },
    gemini:      { zh: 'Google Gemini',   en: 'Google Gemini' },
    groq:        { zh: 'Groq',            en: 'Groq' },
    huggingface: { zh: 'HuggingFace',     en: 'HuggingFace' },
    hunyuan:     { zh: '腾讯混元',         en: 'Tencent Hunyuan' },
    mimo:        { zh: '小米 MiMo',        en: 'Xiaomi MiMo' },
    mistral:     { zh: 'Mistral',         en: 'Mistral' },
    modelscope:  { zh: '魔搭 ModelScope',  en: 'ModelScope' },
    nvidia:      { zh: 'NVIDIA NIM',      en: 'NVIDIA NIM' },
    ollama:      { zh: 'Ollama（本地）',    en: 'Ollama (local)' },
    openrouter:  { zh: 'OpenRouter',      en: 'OpenRouter' },
    qianfan:     { zh: '百度千帆',         en: 'Baidu Qianfan' },
    qwen:        { zh: '通义千问',         en: 'Qwen (Alibaba)' },
    sambanova:   { zh: 'SambaNova',       en: 'SambaNova' },
    siliconflow: { zh: '硅基流动',         en: 'SiliconFlow' },
    spark:       { zh: '讯飞星火',         en: 'iFlytek Spark' },
    together:    { zh: 'Together AI',     en: 'Together AI' },
    zhipu:       { zh: '智谱 BigModel',    en: 'Zhipu BigModel' },
  };
  function lang() {
    return (navigator.language || '').toLowerCase().indexOf('zh') === 0 ? 'zh' : 'en';
  }
  window.OFD = window.OFD || {};
  window.OFD.pname = function (id, langOverride) {
    var m = META[id];
    var l = langOverride || lang();
    if (!m) return id;
    var disp = l === 'zh' ? m.zh : m.en;
    if (disp.toLowerCase() === String(id).toLowerCase()) return id;
    return disp + ' (' + id + ')';
  };
})();
