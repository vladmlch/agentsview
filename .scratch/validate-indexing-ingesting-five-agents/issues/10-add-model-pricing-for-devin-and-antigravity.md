# Add model pricing rules for Devin and Antigravity models

Type: task
Status: resolved
Blocked by: 

## Question

How should AgentsView update `model_pricing` / pricing seed catalogs to:
1. Map Cognition Devin CLI internal models (`swe-2-max`, `swe-2-high`, `swe-2-medium`, `swe-1-6-slow`, `gpt-6-luna-xhigh-priority`) to realistic pricing rates or equivalent model tiers?
2. Map Google Antigravity models (`gemini-3.8-flash-high`, `gemini-3.8-flash-exp-a`, `gemini-3.7-flash-high`, `gemini-3.8-flash-medium`, `claude-opus-4-6-thinking`) to their standard microdollar rates so token counts reflect accurate dollar costs?

## Answer

1. **Devin Model Pricing**: In `internal/pricing/supplemental.go` and `internal/pricing/normalize.go`, Devin CLI internal model names (`swe-2-max`, `swe-2-high`, `swe-2-medium`, `swe-1-6-slow`) are mapped to fixed alias rates and canonical representations, while `gpt-6-luna-xhigh-priority` is normalized to the GPT-6 Luna pricing rules.
2. **Antigravity Model Pricing**: Seeded pricing rules in `supplemental.go` (bumping supplemental catalog to version 6) for `gemini-3.8-flash-*`, `gemini-3.7-flash-*`, and `claude-opus-4-6-thinking`. Microdollar rates per million tokens now allow dynamic calculation to produce accurate non-zero cost totals across the 600M+ tokens recorded.
