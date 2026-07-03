# Token Guard AI — Operations Runbook

Ops guide for self-hosted deployments. For client setup, see [ONBOARDING.md](../ONBOARDING.md).

---

## Enforcement Modes

| Mode | When to use |
|------|-------------|
| `off` | Local dev, passthrough testing only |
| `shadow` | First 24–48h in production — reserve + settle + log, never block |
| `enforce` | After shadow validation — return 429 when budget exhausted |

**Never jump straight to `enforce` in production.**

---

## Shadow → Enforce Rollout

1. Deploy with `ENFORCEMENT_MODE=shadow`
2. Run production traffic for 24–48h
3. Compare admin API balances against expected usage
4. Check logs for unexpected `fail_open` or `missing_usage` outcomes
5. Set `ENFORCEMENT_MODE=enforce` and recreate proxy container
6. Monitor 429 rate and Slack WARN alerts

Rollback: set `ENFORCEMENT_MODE=shadow` — immediate, no data loss.

---

## Fail-Open Behavior

When Redis is unreachable or pre-check times out (>50ms):

- LLM requests **continue to the provider** (never blocked)
- `fail_open_total` metric increments
- Slack **CRITICAL** alert fires (if configured)
- `/readyz` returns 503 (orchestration signal)

**Financial protection is paused during fail-open.** Fix Redis ASAP.

---

## Health Endpoints

| Endpoint | Meaning |
|----------|---------|
| `GET /healthz` | Process alive — always 200 |
| `GET /readyz` | Redis reachable — 503 if not |

Note: proxy continues serving LLM traffic even when `/readyz` is 503.

---

## Admin API

Requires `Authorization: Bearer $ADMIN_API_KEY`.

```bash
# Check balance
curl http://localhost:8080/admin/v1/buckets/my-bucket \
  -H "Authorization: Bearer $ADMIN_API_KEY"

# Set absolute balance
curl -X PUT http://localhost:8080/admin/v1/buckets/my-bucket \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"balance": 100000}'

# Add tokens
curl -X POST http://localhost:8080/admin/v1/buckets/my-bucket/topup \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"amount": 50000}'
```

---

## Provider Smoke Tests

Run after deploy or config change.

### OpenAI

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Budget-Bucket-Id: smoke-test" \
  -d '{"model":"gpt-4o-mini","max_tokens":50,"messages":[{"role":"user","content":"say hi"}]}'
```

Check balance decreased via admin API.

### Anthropic (separate proxy instance with Anthropic upstream)

```bash
curl -X POST http://localhost:8080/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -H "X-Budget-Bucket-Id: smoke-test" \
  -d '{"model":"claude-3-5-haiku-20241022","max_tokens":50,"messages":[{"role":"user","content":"say hi"}]}'
```

---

## Alerts (Slack)

| Event | Severity | Action |
|-------|----------|--------|
| Fail-open bypass | CRITICAL | Check Redis connectivity immediately |
| Budget denied (429) | WARN | Top up bucket or investigate usage spike |

---

## Troubleshooting

### Balance stuck (not decreasing after successful requests)

- Confirm `ENFORCEMENT_MODE` is not `off`
- Check logs for `outcome=settled` vs `missing_usage`
- Verify correct provider extractor (`UPSTREAM_HOST`)

### Balance lower than expected

- Reservations may be held until settle completes
- Check for upstream 4xx/5xx (should trigger release/refund)
- Unsettled reservations expire after `RESERVATION_TTL_SEC` (default 300s)

### High fail_open rate

- Redis latency or connectivity — check `REDIS_URL`, network, memory
- Increase `PRECHECK_TIMEOUT_MS` only as last resort ( increases fail-open window)

---

## Key Environment Variables

See `.env.example` and [README.md](../README.md) for full list.

| Variable | Production recommendation |
|----------|---------------------------|
| `ENFORCEMENT_MODE` | `shadow` → then `enforce` |
| `ADMIN_API_KEY` | Strong random secret; rotate periodically |
| `SLACK_WEBHOOK_URL` | Set in production |
| `RESERVATION_TTL_SEC` | 300 (default) |
| `PRECHECK_TIMEOUT_MS` | 50 (default) |

---

## Backup & Recovery

- **Budget data:** Redis keys `budget:{bucket_id}`. Back up Redis (RDB/AOF) if budgets matter.
- **Reservations:** Ephemeral; expire via TTL. No manual recovery needed.
- **Proxy:** Stateless — redeploy from image anytime.

---

## Security Checklist

- [ ] `ADMIN_API_KEY` set and not committed to git
- [ ] `X-Budget-Bucket-Id` injected at gateway, not by end users
- [ ] TLS termination in front of proxy (reverse proxy / load balancer)
- [ ] Redis not exposed to public internet
- [ ] Provider API keys not logged
