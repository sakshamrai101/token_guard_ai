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
   - `ENFORCEMENT_MODE=shadow` (then `enforce` after soak)
   - `UPSTREAM_URL` / `UPSTREAM_HOST` for your provider
   - Optional: Stripe + `SLACK_WEBHOOK_URL`
4. Open firewall for `8080` (or terminate TLS on nginx/Caddy → `127.0.0.1:8080`).
5. `docker compose up -d --build`
6. Point Stripe webhook to `https://your.domain/billing/webhook` (or use Stripe CLI while testing).
7. Create org → key → seed budget → send a test LLM call (steps below).
8. Confirm `/ops` and `/readyz`.

Do **not** expose Redis or Postgres publicly.

---

## Hosted onboarding (org → key → Slack → Checkout → /ops)

All admin calls use `Authorization: Bearer $ADMIN_API_KEY`.

### 1. Create org

```bash
curl -s -X POST http://localhost:8080/admin/v1/orgs \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"Acme"}'
# → {"id":"org_…","name":"Acme","plan":"trial",…}
export ORG_ID=org_…   # from response
```

### 2. Create TokenGuard API key (raw key shown once)

```bash
curl -s -X POST http://localhost:8080/admin/v1/orgs/$ORG_ID/keys \
  -H "Authorization: Bearer $ADMIN_API_KEY"
# → {"key":"tg_…","warning":"store this key now; …"}
export TG_KEY=tg_…
```

### 3. Optional: per-org Slack webhook

```bash
curl -s -X PATCH http://localhost:8080/admin/v1/orgs/$ORG_ID \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"slack_webhook_url":"https://hooks.slack.com/services/..."}'
```

### 4. Seed budget (org-scoped Redis key)

```bash
curl -s -X PUT "http://localhost:8080/admin/v1/buckets/my-app?org_id=$ORG_ID" \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"balance": 50000}'
```

### 5. Optional: Stripe Checkout (Indie / Team)

Requires `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `STRIPE_PRICE_INDIE`, `STRIPE_PRICE_TEAM`.

```bash
# Local webhook forward
stripe listen --forward-to localhost:8080/billing/webhook
# put printed whsec_… into STRIPE_WEBHOOK_SECRET and recreate proxy

curl -s -X POST http://localhost:8080/admin/v1/orgs/$ORG_ID/checkout \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"plan":"indie"}'
# → {"url":"https://checkout.stripe.com/…"}  open in browser
```

On `checkout.session.completed`, org `plan` becomes `indie` or `team`. Subscription deleted → `trial`.

### 6. Ops page

Browser: `http://localhost:8080/ops`  
Username: `admin` · Password: value of `ADMIN_API_KEY`

```bash
curl -u "admin:$ADMIN_API_KEY" http://localhost:8080/ops
```

### 7. Customer LLM call

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "X-TokenGuard-Key: $TG_KEY" \
  -H "X-Budget-Bucket-Id: my-app" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'
```

Missing/invalid `X-TokenGuard-Key` → **401** (when Postgres / multi-tenant is enabled).

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

## Security Checklist

- [ ] `ADMIN_API_KEY` set and not committed
- [ ] Redis and Postgres not public
- [ ] TLS in front of proxy
- [ ] Customers use `X-TokenGuard-Key`; provider `Authorization` is passthrough only
- [ ] Prefer gateway-injected `X-Budget-Bucket-Id`
- [ ] Provider secrets never logged
