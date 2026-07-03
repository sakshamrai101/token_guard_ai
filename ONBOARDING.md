# Client Onboarding Guide

How to deploy Token Guard AI and walk a client through setup. Use this for sales demos, design partners, and production rollout.

For technical architecture, see [ARCHITECTURE.md](ARCHITECTURE.md). For builder roadmap, see [PLAN.md](PLAN.md).

---

## What You're Selling

> A drop-in proxy that sits between your app and OpenAI/Anthropic. Set a token budget per customer, feature, or environment. When the budget is exhausted, requests return **429** instead of hitting your API bill. If Redis goes down, your app **keeps working** (fail-open).

**Budget unit:** raw token count (matches provider `usage` metadata).

**What Token Guard is NOT (v1):** a user dashboard, billing system, or API key manager.

---

## Architecture

```text
Your App  →  [Your API Gateway]  →  Token Guard Proxy  →  OpenAI / Anthropic
                  │                         │
                  │ injects                   │ Redis
                  ▼                         ▼
          X-Budget-Bucket-Id          budget:{bucket_id}
          X-Request-Id
          Authorization (passthrough)
```

- Provider API keys pass through — Token Guard does not store them.
- Bucket IDs must be injected by **your gateway or middleware**, not by end users.

---

## 30-Minute Setup

### Step 1 — Deploy (5 min)

```bash
git clone <repo>
cd token_guard_ai
cp .env.example .env
# Edit .env: set ADMIN_API_KEY, ENFORCEMENT_MODE=shadow, SLACK_WEBHOOK_URL (optional)
docker compose up -d
```

Verify health:

```bash
curl http://localhost:8080/healthz   # {"status":"ok"}
curl http://localhost:8080/readyz    # {"status":"ready"}
```

### Step 2 — Seed a budget (2 min)

```bash
export ADMIN_API_KEY=your-secret-key

curl -X PUT http://localhost:8080/admin/v1/buckets/acme-corp \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"balance": 500000}'

curl http://localhost:8080/admin/v1/buckets/acme-corp \
  -H "Authorization: Bearer $ADMIN_API_KEY"
# {"bucket_id":"acme-corp","balance":500000}
```

### Step 3 — Point your app at the proxy (5 min)

| Before | After |
|--------|-------|
| `https://api.openai.com/v1/...` | `http://token-guard:8080/v1/...` |
| `https://api.anthropic.com/v1/...` | separate proxy instance with `UPSTREAM_URL=https://api.anthropic.com` |

No SDK changes — same request bodies and headers (plus budget headers below).

### Step 4 — Inject headers (10 min)

Add at your gateway or app middleware:

| Header | Value | Required |
|--------|-------|----------|
| `X-Budget-Bucket-Id` | e.g. `acme-corp`, `user-123`, `feature-chat` | Yes (for enforcement) |
| `X-Request-Id` | UUID per request | Optional (auto-generated if omitted) |
| `Authorization` | Provider API key | Yes (passthrough) |

**Security:** Never let end users set `X-Budget-Bucket-Id` directly.

### Step 5 — Shadow mode validation (24–48h)

Keep `ENFORCEMENT_MODE=shadow`. Token Guard will:

- Reserve and settle budgets
- Log all decisions
- **Never block** requests (even if budget exhausted)

Monitor:

```bash
# Check balance after traffic
curl http://localhost:8080/admin/v1/buckets/acme-corp \
  -H "Authorization: Bearer $ADMIN_API_KEY"
```

Balance should decrease by actual `usage.total_tokens` per completion.

### Step 6 — Enforce (when confident)

```bash
# In .env or docker-compose
ENFORCEMENT_MODE=enforce
docker compose up -d --force-recreate proxy
```

Exhausted buckets now return **429**:

```json
{"error":{"message":"budget exhausted","type":"budget_exceeded"}}
```

Your app should handle 429 gracefully (retry later, show quota message, etc.).

### Step 7 — Wire alerts

Set `SLACK_WEBHOOK_URL` for:

- **CRITICAL:** fail-open (Redis down — enforcement paused, traffic still flows)
- **WARN:** budget denied (429 — bucket exhausted)

---

## Rollout Checklist

- [ ] Docker Compose running, `/healthz` and `/readyz` OK
- [ ] Admin API secured with strong `ADMIN_API_KEY`
- [ ] Budgets seeded for all active buckets
- [ ] Gateway injects `X-Budget-Bucket-Id` (not client-supplied)
- [ ] 24–48h in `shadow` mode; balances look correct
- [ ] Slack alerts configured
- [ ] App handles 429 responses
- [ ] Promoted to `enforce`

---

## Honest Limitations (tell clients upfront)

| Topic | Reality |
|-------|---------|
| Budget unit | Tokens, not dollars |
| Fail-open | Redis outage = temporary loss of protection (traffic still works) |
| Concurrency | Brief overshoot possible: up to `concurrent_requests × reservation_estimate` |
| Providers | OpenAI + Anthropic (v1); one upstream URL per proxy instance |
| Multi-tenancy | Bucket strings in Redis — no built-in user accounts (v1) |

Transparency builds trust and prevents bad word of mouth.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| All requests pass despite empty budget | `ENFORCEMENT_MODE=shadow` or `off` | Set `enforce` |
| Balance not decreasing | Settlement not running or wrong provider extractor | Check logs for `outcome=settled`; verify `UPSTREAM_HOST` |
| 429 but budget looks fine | Reservations held (unsettled) | Wait for TTL or check upstream errors |
| Requests pass when Redis down | Fail-open (by design) | Fix Redis; check Slack CRITICAL alert |
| Balance wrong after errors | Release should refund on 4xx/5xx | Check upstream status codes in logs |

See [docs/RUNBOOK.md](docs/RUNBOOK.md) for ops details (created during Launch-2).

---

## Demo Script (15 min sales call)

1. Show Docker Compose up in one command
2. Seed `demo-bucket` with 5000 tokens via admin API
3. Send 3 chat completions through proxy (shadow or enforce)
4. Show balance dropped by exact usage via admin GET
5. Set balance to 100, send large request → show 429
6. Mention fail-open: "Even if Redis dies, your app keeps working — you get a Slack alert"

---

## When They Need More (v2 conversation)

- "We don't want to run Docker" → managed hosted service (Postgres + dashboard)
- "We need dollar budgets" → cost table per model (post-v1)
- "We need Gemini" → additional extractor (post-v1)
- "We need audit history" → Postgres audit log (hosted v2)
