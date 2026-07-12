# Client Onboarding Guide

How customers use **hosted TokenGuard** (your multi-tenant proxy). For operator deploy and Stripe/Slack setup, see [docs/RUNBOOK.md](docs/RUNBOOK.md). Architecture: [ARCHITECTURE.md](ARCHITECTURE.md) §14.

---

## What You're Selling

> Point your OpenAI/Anthropic SDK at TokenGuard. Set a token budget. Get Slack when you're about to blow it. We settle usage in real time and return **429** when the budget is gone — without taking your app down if Redis hiccups (fail-open).

**Budget unit:** raw tokens.  
**You keep your provider API keys** — they pass through; we never store them.

---

## Architecture (customer view)

```text
Your App  →  TokenGuard Proxy (hosted)  →  OpenAI / Anthropic
               │
               ├─ X-TokenGuard-Key: tg_...     (your TokenGuard key)
               ├─ Authorization / x-api-key    (provider key, passthrough)
               └─ X-Budget-Bucket-Id           (e.g. prod-chat)
```

Redis holds `budget:{org_id}:{bucket_id}`. Postgres holds orgs, keys, usage history.

---

## Customer setup (after you give them a key)

### 1. What you receive from the operator

- Proxy base URL, e.g. `https://proxy.tokenguard.ai`
- TokenGuard API key: `tg_...` (shown once — store securely)
- Bucket name to use, e.g. `prod-chat`
- Optional: Slack channel already wired to your org

### 2. Point the SDK at TokenGuard

**OpenAI (Python):**

```python
from openai import OpenAI

client = OpenAI(
    api_key=os.environ["OPENAI_API_KEY"],           # your OpenAI key
    base_url="https://proxy.tokenguard.ai/v1",
    default_headers={
        "X-TokenGuard-Key": os.environ["TG_KEY"],
        "X-Budget-Bucket-Id": "prod-chat",
    },
)

r = client.chat.completions.create(
    model="gpt-4o-mini",
    max_tokens=50,
    messages=[{"role": "user", "content": "hi"}],
)
print(r.usage)
```

**curl:**

```bash
curl -X POST https://proxy.tokenguard.ai/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "X-TokenGuard-Key: $TG_KEY" \
  -H "X-Budget-Bucket-Id: prod-chat" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'
```

**Anthropic:** use a proxy instance with `UPSTREAM_HOST=api.anthropic.com`, same TokenGuard headers, Anthropic `x-api-key` + `anthropic-version`.

### 3. What happens at runtime

| Step | Behavior |
|------|----------|
| Auth | Invalid `tg_` key → **401** |
| Reserve | Tokens held in Redis before upstream call |
| Success | Usage settled; balance drops by actual tokens |
| Exhausted (`enforce`) | **429** + Slack `budget_exhausted` |
| Low budget | Slack `budget_warning_80` (at most once/hour) |
| Redis down | Request still reaches provider (fail-open) + Slack CRITICAL |

### 4. Rollout tip

Start with operator in `ENFORCEMENT_MODE=shadow` (logs/settles, never blocks), then promote to `enforce`.

---

## Honest limitations (tell customers)

| Topic | Reality |
|-------|---------|
| Setup | Operator mints your key (self-serve signup is post-v1) |
| Dashboard | Operator `/ops` for now — you get Slack + they can dump usage |
| Providers | One upstream per proxy (OpenAI **or** Anthropic) |
| Plan quotas | Indie/Team are billing plans; soft limits not hard-enforced in proxy yet |
| Bucket header | You must send `X-Budget-Bucket-Id` every request |

---

## Demo script (15 min)

1. Show `/ops` after a few calls (balances drop by real usage)
2. Exhaust budget → 429 + Slack
3. Top up via admin → next call works
4. Stop Redis briefly → app still gets LLM response (fail-open)

---

## Operator path

Full create-org / Stripe / VPS steps: [docs/RUNBOOK.md](docs/RUNBOOK.md).
