# Built-in Provider Directory

Last regenerated: 2026-09-05

OmniFusion currently ships 24 built-in provider declarations and also accepts custom OpenAI-compatible provider declarations. This table reflects the repository registry; upstream quotas and model availability can change at any time. Always confirm the provider's current terms before production use.

| Provider | Declared free-tier / billing boundary | Declared window limits |
|---|---|---|
| Anthropic | No free tier; BYOK paid usage | - |
| Ark / Doubao | No fixed free tier; limited-time credits may exist | - |
| Cerebras | Free trial: 1M tokens/day, no card | 5 RPM, 30K TPM |
| Chutes | Free tier is rate-limited; some models are permanently free | Provider/model-specific |
| Cloudflare Workers AI | 10K neurons/day, approximately 150 LLM responses | ~150 RPD |
| Cohere | Trial key: 1000 calls/month, non-production use | 20 RPM |
| DeepSeek | No free tier; BYOK paid usage | - |
| Google Gemini | AI Studio free tier, model-specific quotas | Model-specific |
| Groq | Free tier, no card | 30 RPM, 14.4K RPD |
| HuggingFace Router | Monthly free allowance for Inference Providers | Account/model-specific |
| Tencent Hunyuan | 1M tokens/year | Console-specific |
| Xiaomi MiMo | Pay-as-you-go with signup credit | - |
| Mistral La Plateforme | Experiment free tier, low rate | Account-specific |
| ModelScope | 2000 free calls/day | 2000 RPD |
| NVIDIA NIM | Free developer allowance, account-credit based | ~40 RPM |
| Ollama | Local inference, no cloud quota | Local resource limits |
| OpenRouter | `:free` models; 20 RPM / 50 RPD before 10 lifetime credits, 20 RPM / 1000 RPD after | 20 RPM, 50 RPD conservative default |
| Baidu Qianfan | ERNIE Speed/Lite families can be free after console activation | Console-specific |
| Alibaba Qwen / Bailian | New-account model-specific tokens for 90 days | Model-specific |
| SambaNova Cloud | Developer tier: shared daily token allowance | 20M TPD |
| SiliconFlow | L0 free tier with 16 models after identity verification | Account/model-specific |
| iFlytek Spark | Spark Lite permanently free with rate limits | Account-specific |
| Together AI | No free tier; BYOK paid usage | - |
| Zhipu BigModel | GLM Flash families free with concurrency limits | Account-specific |

The source of truth for these declarations is `internal/provider/registry/providers/`. To add a provider, start from an existing YAML declaration and include the official signup URL, authentication style, model capabilities, free-tier boundary, and rate-limit evidence.
