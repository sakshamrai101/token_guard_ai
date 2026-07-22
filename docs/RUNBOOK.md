# Token Guard AI — Operations Runbook

Ops guide for self-hosted and **hosted multi-tenant** deployments. For client SDK setup, see [ONBOARDING.md](../ONBOARDING.md).

---

## Docker Compose smoke (local / VPS)

```bash
cp .env.example .env
# Edit .env — set a strong ADMIN_API_KEY (required)

docker compose up -d --build

curl -sf http://localhost:8080/healthz   # {"status":"ok"}
curl -sf http://localhost:8080/readyz    # {"status":"ready"}
docker compose ps                        # proxy, redis, postgres healthy
```

Compose runs **proxy + redis + postgres**. Schema (orgs, api_keys, buckets, usage_events, Stripe columns) auto-migrates on proxy startup when `DATABASE_URL` is set (Compose injects it).

With Postgres configured, LLM calls require `X-TokenGuard-Key`.

Tear down: `docker compose down` (add `-v` to wipe the Postgres volume).

---

## VPS deploy (outline)

1. Provision a VPS; install Docker + Compose.
2. Clone the repo; `cp .env.example .env`.
3. Set at least:
   - `ADMIN_API_KEY` — long random secret
   - `PUBLIC_BASE_URL` — public browser URL (e.g. `https://proxy.yourdomain`)
   - `ENFORCEMENT_MODE=shadow` (then `enforce` after soak)
   - `UPSTREAM_URL` / `UPSTREAM_HOST` for legacy unprefixed paths
   - Optional: `OPENAI_UPSTREAM_*` / `ANTHROPIC_UPSTREAM_*` (defaults enable `/openai` + `/anthropic`)
   - Stripe (`STRIPE_*`) for self-serve signup; optional `SLACK_WEBHOOK_URL`
   - `TRIAL_BUDGET_TOKENS` (default `200000`) if you want a non-default seed
4. Open firewall for `8080` (or terminate TLS on nginx/Caddy → `127.0.0.1:8080`).
5. `docker compose up -d --build`
6. Point Stripe webhook to `https://your.domain/billing/webhook` (or use Stripe CLI while testing).
7. Confirm `/signup`, `/account`, `/ops`, and `/readyz`. Smoke a Checkout → `/setup` reveal → LLM call → `/me/buckets`.

Do **not** expose Redis or Postgres publicly.

---

## Self-serve onboarding (primary)

Customers never need operator curl.

1. Set Stripe env vars. Success URL **must** include `{CHECKOUT_SESSION_ID}`:

   ```
   STRIPE_SUCCESS_URL={PUBLIC_BASE_URL}/setup?session_id={CHECKOUT_SESSION_ID}
   STRIPE_CANCEL_URL={PUBLIC_BASE_URL}/signup?canceled=1
   ```

   If unset, defaults derive from `PUBLIC_BASE_URL`.

2. Local webhook forward:

   ```bash
   stripe listen --forward-to localhost:8080/billing/webhook
   # put printed whsec_… into STRIPE_WEBHOOK_SECRET and recreate proxy
   ```

3. Browser: `http://localhost:8080/signup` → email + plan → Checkout.

4. After payment, Stripe redirects to `/setup?session_id=cs_…`. Copy the `tg_` key **once** (optional Slack form). Second visit → already revealed / expired.

5. Webhook (`checkout.session.completed`) provisions: org (by email), hashed key, `default` bucket, Redis `budget:{org_id}:default` = `TRIAL_BUDGET_TOKENS`, one-time setup secret TTL 15m.

6. Customer LLM call (bucket header optional):

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "X-TokenGuard-Key: $TG_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'
```

Missing/invalid `X-TokenGuard-Key` → **401** (when Postgres / multi-tenant is enabled).

### Ops page

Browser: `http://localhost:8080/ops`  
Username: `admin` · Password: value of `ADMIN_API_KEY`

```bash
curl -u "admin:$ADMIN_API_KEY" http://localhost:8080/ops
```

---

## Support fallback (admin mint)

Use when a customer lost their one-time key or needs a manual org. All admin calls use `Authorization: Bearer $ADMIN_API_KEY`.

### 1. Create org + key + seed

```bash
ORG_ID=$(curl -s -X POST http://localhost:8080/admin/v1/orgs \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"Acme"}' | jq -r .id)

TG_KEY=$(curl -s -X POST http://localhost:8080/admin/v1/orgs/$ORG_ID/keys \
  -H "Authorization: Bearer $ADMIN_API_KEY" | jq -r .key)

curl -s -X PUT "http://localhost:8080/admin/v1/buckets/default?org_id=$ORG_ID" \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"balance": 200000}'
```

### 2. Optional: Slack + admin Checkout upgrade

```bash
curl -s -X PATCH http://localhost:8080/admin/v1/orgs/$ORG_ID \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"slack_webhook_url":"https://hooks.slack.com/services/..."}'

curl -s -X POST http://localhost:8080/admin/v1/orgs/$ORG_ID/checkout \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"plan":"indie"}'
```

Admin Checkout still works (`metadata.org_id`). Public signup uses email metadata. On `customer.subscription.deleted`, plan returns to `trial`.

---

## Enforcement Modes

| Mode | When to use |
|------|-------------|
| `off` | Local passthrough testing only |
| `shadow` | First 24–48h — reserve + settle + log, never block |
| `enforce` | After validation — **429** when budget exhausted |

**Never jump straight to `enforce` in production.**

### Shadow → Enforce

1. Deploy with `ENFORCEMENT_MODE=shadow`
2. Run traffic 24–48h; compare `/ops` / admin balances to expected usage
3. Watch logs for unexpected `fail_open` / `missing_usage`
4. Set `ENFORCEMENT_MODE=enforce` and recreate proxy
5. Monitor 429 rate and Slack alerts

Rollback: set `ENFORCEMENT_MODE=shadow` — immediate, no data loss.

---

## Fail-Open Behavior

When Redis is unreachable or pre-check times out (>50ms):

- LLM requests **continue to the provider**
- `fail_open_total` increments; Slack **CRITICAL** (org webhook preferred)
- `/readyz` returns 503

Auth failures still return **401** (never fail-open on TokenGuard key).

---

## Health Endpoints

| Endpoint | Meaning |
|----------|---------|
| `GET /healthz` | Process alive — always 200 |
| `GET /readyz` | Redis reachable — 503 if not |

Proxy still serves LLM traffic fail-open when `/readyz` is 503.

---

## Admin API (operator)

Requires `Authorization: Bearer $ADMIN_API_KEY`. Not proxied upstream.

| Method | Path | Notes |
|--------|------|-------|
| GET/PUT | `/admin/v1/buckets/{id}?org_id=` | Balance get/set |
| POST | `/admin/v1/buckets/{id}/topup?org_id=` | Add tokens |
| GET | `/admin/v1/buckets`, `/usage`, `/reservations` | Dump APIs |
| POST/GET | `/admin/v1/orgs` | Create / list orgs |
| PATCH | `/admin/v1/orgs/{id}` | Set `slack_webhook_url` |
| POST | `/admin/v1/orgs/{id}/keys` | Mint `tg_` key (once) |
| POST | `/admin/v1/orgs/{id}/checkout` | Stripe Checkout URL |

---

## Alerts (Slack)

| Event | Severity | Notes |
|-------|----------|-------|
| `fail_open` | CRITICAL | Fix Redis |
| `budget_exhausted` | WARN | Top up or investigate |
| `budget_warning_80` | WARN | ≤20% remaining; deduped 1h/org+bucket |

Per-org webhook preferred; else global `SLACK_WEBHOOK_URL`.

---

## Troubleshooting

### `/ops` returns 404

- `ADMIN_API_KEY` must be set (not `ADMIN_KEY`) and proxy restarted
- Look for log: `ops page mounted`

### Balance stuck

- Confirm `ENFORCEMENT_MODE` ≠ `off`
- Logs: `outcome=settled` vs `missing_usage`
- Correct `UPSTREAM_HOST` for extractors

### High fail_open rate

- Check Redis latency / `REDIS_URL` / memory

---

## Backup & Recovery

- **Budgets:** Redis keys `budget:{org_id}:{bucket_id}` — enable RDB/AOF
- **Orgs / keys / usage / Stripe:** Postgres volume `pgdata` — snapshot regularly
- **Reservations:** TTL-expiring; no backup needed
- **Proxy:** Stateless — redeploy from image anytime

---

## Customer analytics vs operator ops

| Who | Surface | Auth |
|-----|---------|------|
| Customer | `/me/*`, `/account` | `X-TokenGuard-Key` — org-scoped balances/usage + Slack update |
| Operator | `/ops`, `/admin/*` | `ADMIN_API_KEY` — cross-tenant support |

Point customers at `/account` (or `GET /me/buckets`) for “what’s my balance?” — **never** share operator Basic auth.

```bash
curl -s http://localhost:8080/me/buckets -H "X-TokenGuard-Key: $TG_KEY"
curl -s "http://localhost:8080/me/usage?limit=50" -H "X-TokenGuard-Key: $TG_KEY"
```

**Out of A1:** React dashboard, customer topup.

---

## Multi-provider vs single-upstream

| Mode | How |
|------|-----|
| Single provider (legacy) | `UPSTREAM_URL` / `UPSTREAM_HOST`; clients use `/v1/…` |
| Dual OpenAI + Anthropic | Path prefixes `/openai/…` and `/anthropic/…` on one process; `OPENAI_UPSTREAM_*` + `ANTHROPIC_UPSTREAM_*` (defaults work); same `tg_` key + buckets |

Smoke: one OpenAI + one Anthropic completion through prefixes; confirm `/account` or admin balance dropped by both usage totals.

**Out of M1:** Gemini/Grok, body-based routing.

---

## Security Checklist

- [ ] `ADMIN_API_KEY` set and not committed
- [ ] Redis and Postgres not public
- [ ] TLS in front of proxy
- [ ] Customers use `X-TokenGuard-Key`; provider `Authorization` is passthrough only
- [ ] `PUBLIC_BASE_URL` + Stripe success URL include `{CHECKOUT_SESSION_ID}` for `/setup`
- [ ] Bucket header optional after signup (`default`); prefer gateway injection for multi-bucket apps
- [ ] Provider secrets never logged
- [ ] Never give customers `ADMIN_API_KEY`; they use `/account` + `/me`
