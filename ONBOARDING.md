# Client Onboarding Guide

Self-serve hosted TokenGuard. Operator deploy: [docs/RUNBOOK.md](docs/RUNBOOK.md). Spec: [PLAN.md](PLAN.md) **S1**. Architecture: [ARCHITECTURE.md](ARCHITECTURE.md) §15.

---

## What You're Selling

> Start on the site → Checkout → copy your API key once → point your SDK at TokenGuard. Budgets + Slack when spend is about to blow. **429** when exhausted; fail-open if Redis hiccups.

Provider keys passthrough — never stored.

---

## Customer path (self-serve)

1. Open `/signup` → email + plan (`trial` / `indie` / `team`) → Stripe Checkout  
2. Land on `/setup?session_id=...` → **copy `tg_` key once** (optional Slack webhook)  
3. Point SDK (bucket header optional — seeded `default` bucket is used when omitted):

```python
from openai import OpenAI
import os

client = OpenAI(
    api_key=os.environ["OPENAI_API_KEY"],
    base_url="https://proxy.yourdomain/v1",  # or PUBLIC_BASE_URL + "/v1"
    default_headers={
        "X-TokenGuard-Key": os.environ["TG_KEY"],
        # optional — omit to charge the seeded "default" bucket:
        # "X-Budget-Bucket-Id": "default",
    },
)
```

```bash
curl -X POST "$PUBLIC_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "X-TokenGuard-Key: $TG_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'
```

4. Call models as usual.

| Runtime | Behavior |
|---------|----------|
| Bad `tg_` key | **401** |
| Budget gone (`enforce`) | **429** + Slack |
| Low budget | Slack 80% warning |
| Redis down | Fail-open to provider + Slack CRITICAL |

**Key reveal is one-time.** Second visit to `/setup` shows “already revealed or expired” — contact support (admin mint) if lost.

---

## Operator notes

- Self-serve is primary; admin mint org/key is **support fallback** only  
- `/ops` = your view of balances/usage  
- Stripe webhook must hit `/billing/webhook`  
- Set `PUBLIC_BASE_URL` and `TRIAL_BUDGET_TOKENS` in `.env`  
- Stripe success URL must be `{PUBLIC_BASE_URL}/setup?session_id={CHECKOUT_SESSION_ID}` (default when unset)
