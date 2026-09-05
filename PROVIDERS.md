# Built-in Provider Matrix

Last regenerated: 2026-09-05

OmniFusion currently ships 24 built-in provider declarations and accepts custom OpenAI-compatible, Anthropic, and Gemini provider declarations. The goal is continuous global free-provider aggregation; the table below describes what this repository currently ships. Upstream quotas and model availability change frequently, so always confirm the provider's current terms before production use.

Regenerate this page after changing a declaration:

```bash
go run ./scripts/provider-matrix -date YYYY-MM-DD -out PROVIDERS.md
```

| Provider | Protocol | Declared free tier / billing boundary | Declared window limits | Models | Capabilities | Key signup |
|---|---|---|---|---|---|---|
| Anthropic | Anthropic | 无免费层（BYOK 付费） | Provider/account-specific | 4 declared / 0 explicit-free | in: text/image; out: text; tools | [keys](https://console.anthropic.com/settings/keys) |
| 火山方舟（豆包） | OpenAI-compatible | 无固定免费层（部分模型限时有赠额，BYOK 为主） | Provider/account-specific | 3 declared / 0 explicit-free | in: text; out: text; tools | [keys](https://console.volcengine.com/ark) |
| Cerebras | OpenAI-compatible | 免费试用 1M tokens/天、30K TPM、5 RPM（无卡） | 5 RPM, 30K TPM, 1M TPD | 3 declared / 3 explicit-free | in: text; out: text; tools | [keys](https://cloud.cerebras.ai) |
| Chutes | OpenAI-compatible | 免费档（速率受限，超限 429；部分模型永久免费） | Provider/account-specific | - | in: text; out: text; tools | [keys](https://chutes.ai) |
| Cloudflare Workers AI | OpenAI-compatible | 10K neurons/天（约 150 次 LLM 响应） | 150 RPD | 4 declared / 4 explicit-free | in: text/image; out: text; tools | [keys](https://dash.cloudflare.com) |
| Cohere | OpenAI-compatible | Trial key 免费：1000 次/月、20 次/分钟（禁生产用途） | 20 RPM | - | in: text; out: text; tools | [keys](https://dashboard.cohere.com/api-keys) |
| DeepSeek | OpenAI-compatible | 无免费层（BYOK 付费，充值计费） | Provider/account-specific | 2 declared / 0 explicit-free | in: text; out: text; tools | [keys](https://platform.deepseek.com/api_keys) |
| Google Gemini | Gemini | AI Studio 免费层（按模型配额） | Provider/account-specific | 3 declared / 3 explicit-free | in: text/image/audio; out: text; tools | [keys](https://aistudio.google.com/app/apikey) |
| Groq | OpenAI-compatible | 免费层 30 RPM / 14.4K RPD（无卡） | 30 RPM, 14400 RPD | 6 declared / 6 explicit-free | in: text/image; out: text; tools | [keys](https://console.groq.com/keys) |
| HuggingFace Router | OpenAI-compatible | Inference Providers 免费额度（$0.10/月免费层） | Provider/account-specific | 4 declared / 0 explicit-free | in: text/image; out: text; tools | [keys](https://huggingface.co/settings/tokens) |
| 腾讯混元 | OpenAI-compatible | 100 万 tokens/年（控制台口径） | Provider/account-specific | - | in: text; out: text; tools | [keys](https://console.cloud.tencent.com/hunyuan/api-key) |
| 小米 MiMo | OpenAI-compatible | 按量付费（注册赠 ¥10 体验金；官方限免活动另计） | Provider/account-specific | 1 declared / 0 explicit-free | in: text; out: text; tools | [keys](https://mimo.mi.com) |
| Mistral La Plateforme | OpenAI-compatible | Experiment 免费档（低速率，控制台激活） | Provider/account-specific | 2 declared / 0 explicit-free | in: text; out: text; tools | [keys](https://console.mistral.ai/api-keys) |
| 魔搭 ModelScope | OpenAI-compatible | 每日 2000 次免费调用（6 万+ 模型可选） | 2K RPD | 2 declared / 2 explicit-free | in: text; out: text; tools | [keys](https://modelscope.cn/my/tokens) |
| NVIDIA NIM | OpenAI-compatible | 免费开发者额度约 40 RPM（build.nvidia.com，按账户积分） | 40 RPM | 4 declared / 4 explicit-free | in: text/image; out: text; tools | [keys](https://build.nvidia.com) |
| Ollama (本地) | OpenAI-compatible | 本地推理，无配额限制 | Provider/account-specific | - | in: text/image; out: text; tools | - |
| OpenRouter | OpenAI-compatible | :free 后缀模型 $0；未累计购买 10 credits 时 20 RPM / 50 RPD | 20 RPM, 50 RPD | 1 declared / 0 explicit-free | in: text/image; out: text; tools | [keys](https://openrouter.ai/keys) |
| 百度千帆 | OpenAI-compatible | ERNIE-Speed / ERNIE-Lite 系免费（控制台开通；RPM/TPM 见控制台） | Provider/account-specific | - | in: text; out: text; tools | [keys](https://console.bce.baidu.com/qianfan) |
| 通义千问（阿里云百炼） | OpenAI-compatible | 新人限时额度：每模型 100 万 tokens / 90 天（非持续免费层） | Provider/account-specific | 3 declared / 0 explicit-free | in: text; out: text; tools | [keys](https://bailian.console.aliyun.com) |
| SambaNova Cloud | OpenAI-compatible | Developer 档：全模型共享 20M tokens/天（约 20 RPM） | 20M TPD | 7 declared / 7 explicit-free | in: text; out: text; tools | [keys](https://cloud.sambanova.ai) |
| 硅基流动 SiliconFlow | OpenAI-compatible | L0 免费档 16 模型（账户级按模型限速，实名解锁） | Provider/account-specific | 2 declared / 2 explicit-free | in: text; out: text; tools | [keys](https://cloud.siliconflow.cn/account/ak) |
| 讯飞星火 | OpenAI-compatible | Spark Lite 永久免费（并发/速率受限） | Provider/account-specific | 1 declared / 1 explicit-free | in: text; out: text | [keys](https://console.xfyun.cn) |
| Together AI | OpenAI-compatible | 无免费层（BYOK 按量付费） | Provider/account-specific | - | in: text; out: text; tools | [keys](https://api.together.ai/settings/api-keys) |
| 智谱 BigModel | OpenAI-compatible | GLM-4.5-Flash / GLM-4.7-Flash 完全免费（并发限速） | Provider/account-specific | 2 declared / 2 explicit-free | in: text; out: text; tools | [keys](https://open.bigmodel.cn/usercenter/apikeys) |

The source of truth is `internal/provider/registry/providers/`. A row is a routing declaration, not a promise that every account, region, or model is currently usable. Providers without a recurring free tier remain useful for BYOK routing and custom fallback chains.
