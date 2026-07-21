# Token Guard AI

A drop-in LLM API proxy that enforces per-bucket token budgets in real time. Think of it as a **circuit breaker for your OpenAI/Anthropic API key** — block runaway spend before it hits your invoice, without taking down your app when Redis hiccups.

Built for indie devs and small AI startups who want set-and-forget abuse protection. Also ships as a **hosted multi-tenant** stack (Postgres orgs + `tg_` keys + Stripe + Slack + `/ops`).

## What it does

- Transparent reverse proxy to **OpenAI** or **Anthropic** (method, path, body unchanged)
- **Pre-request budget reservation** via atomic Redis Lua scripts
- **Post-response settlement** from provider usage metadata (stream + non-stream)
- **429** when a bucket can't cover the estimated cost (`enforce` mode)
- **Fail-open** when Redis is unreachable — LLM traffic still flows
- **Release** (full refund) on upstream 4xx/5xx
- **Admin API** for buckets, orgs, keys, usage dumps
- **Hosted mode:** `X-TokenGuard-Key`, org-scoped Redis budgets, Stripe plans, Slack alerts, `/account` + `/me`, `/ops`

Provider routing is by `UPSTREAM_HOST`: `api.openai.com` → OpenAI extractors; `api.anthropic.com` → Anthropic extractors. Run one proxy instance per provider.

See [PLAN.md](PLAN.md), [ARCHITECTURE.md](ARCHITECTURE.md), [ONBOARDING.md](ONBOARDING.md), and [docs/RUNBOOK.md](docs/RUNBOOK.md).

**Self-serve (S1):** Customers use `/signup` → Stripe Checkout → `/setup?session_id=` (one-time `tg_` key). Admin mint is support fallback only.

**Customer analytics (A1):** Org-scoped `GET /me/buckets`, `GET /me/usage`, `GET /me/org`, `PATCH /me/slack`, and minimal `/account` HTML (auth: `X-TokenGuard-Key`). Slack stays for alerts; customers should not need operator `/ops`.

## Docker quick start (proxy + Redis + Postgres)

**Requirements:** Docker + Docker Compose

```bash
cp .env.example .env
# Edit .env — set ADMIN_API_KEY to a long random secret

docker compose up -d --build

curl http://localhost:8080/healthz   # {"status":"ok"}
curl http://localhost:8080/readyz    # {"status":"ready"}
docker compose ps                    # proxy, redis, postgres
```

Compose injects `DATABASE_URL` and `REDIS_URL` for in-network hostnames. Schema auto-migrates on startup. With Postgres enabled, **LLM calls require `X-TokenGuard-Key`**.

### Hosted quickstart (self-serve)

Set Stripe + `PUBLIC_BASE_URL` in `.env`, then:

```bash
# Terminal A — webhook forward
stripe listen --forward-to localhost:8080/billing/webhook
# put printed whsec_… into STRIPE_WEBHOOK_SECRET and restart proxy

# Browser: http://localhost:8080/signup  → Checkout → /setup?session_id=…
# Copy tg_ key once from /setup

curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "X-TokenGuard-Key: $TG_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'
```

`X-Budget-Bucket-Id` is optional — seeded bucket `default` is used when omitted.

**Account UI:** [http://localhost:8080/account](http://localhost:8080/account) — paste your `tg_` key to view balances + usage (or call `GET /me/buckets` with `X-TokenGuard-Key`).

**Ops UI:** [http://localhost:8080/ops](http://localhost:8080/ops) — Basic auth user `admin`, password = `ADMIN_API_KEY` (operator only).

Admin mint (org/key/budget) remains for support only — see [docs/RUNBOOK.md](docs/RUNBOOK.md).

### Self-hosted without Postgres

Leave `DATABASE_URL` empty (and remove Compose `DATABASE_URL` override if you customize compose). TokenGuard auth is off; seed buckets without `org_id` (defaults to `default`).

```bash
curl -X PUT http://localhost:8080/admin/v1/buckets/my-app \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"balance": 50000}'

curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Budget-Bucket-Id: my-app" \
  -d '{"model":"gpt-4o","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}'
```

After the response completes, the bucket balance is reconciled to actual `usage.total_tokens` (OpenAI) or `input_tokens + output_tokens` (Anthropic).

## Local development (without Docker Compose)

**Requirements:** Go 1.22+, Redis 7+, optional Postgres 16+

```bash
docker run --rm -p 6379:6379 redis:7-alpine
# optional multi-tenant:
docker run --rm -p 5432:5432 \
  -e POSTGRES_USER=tokenguard -e POSTGRES_PASSWORD=tokenguard -e POSTGRES_DB=tokenguard \
  postgres:16-alpine

export ADMIN_API_KEY=dev-secret
export ENFORCEMENT_MODE=shadow
export REDIS_URL=redis://localhost:6379
export DATABASE_URL='postgres://tokenguard:tokenguard@localhost:5432/tokenguard?sslmode=disable'

go run ./cmd/proxy/
```

## Headers

| Header | Purpose |
|--------|---------|
| `X-TokenGuard-Key` | Hosted auth (`tg_…`); required when `DATABASE_URL` is set |
| `X-Budget-Bucket-Id` | Bucket to charge; optional when org has default (`default` after signup) |
| `X-Request-Id` | Idempotency key (auto-generated UUID if omitted) |

Never put the TokenGuard key in the provider `Authorization` header.

## Enforcement modes

| Mode | Behavior |
|------|----------|
| `off` | Proxy only, no budget checks (default) |
| `shadow` | Reserve + settle + log, never block |
| `enforce` | Block with 429 when budget exhausted |

Start with `shadow` for 24–48h before promoting to `enforce`.

## Admin API

Requires `Authorization: Bearer $ADMIN_API_KEY`. Routes are **not** proxied upstream.

| Method | Path | Body |
|--------|------|------|
| GET | `/admin/v1/buckets/{id}?org_id=` | — |
| PUT | `/admin/v1/buckets/{id}?org_id=` | `{"balance": N}` |
| POST | `/admin/v1/buckets/{id}/topup?org_id=` | `{"amount": N}` |
| GET | `/admin/v1/usage`, `/buckets`, `/reservations` | — |
| POST | `/admin/v1/orgs` | `{"name":"…"}` |
| POST | `/admin/v1/orgs/{id}/keys` | — (returns raw `tg_` once) |
| PATCH | `/admin/v1/orgs/{id}` | `{"slack_webhook_url":"…"}` |
| POST | `/admin/v1/orgs/{id}/checkout` | `{"plan":"indie\|team"}` |

## Settlement behavior

| Scenario | Outcome |
|----------|---------|
| Non-streaming 200 | Sync settle from JSON `usage` on body close |
| Streaming 200 (SSE) | Async settle after final usage chunk |
| Client disconnect | Settle at reserved amount |
| Missing usage metadata | Settle at reserved amount |
| Upstream 4xx/5xx | Release reservation (full refund) |

## Configuration

See [.env.example](.env.example) for all variables. Key settings:

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | Proxy listen address |
| `UPSTREAM_URL` | `https://api.openai.com` | Provider base URL |
| `UPSTREAM_HOST` | `api.openai.com` | Host header rewrite + extractor selection |
| `ENFORCEMENT_MODE` | `off` | `off`, `shadow`, or `enforce` |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection URL |
| `DATABASE_URL` | — | Postgres; enables multi-tenant auth |
| `ADMIN_API_KEY` | — | Bearer for `/admin/*`; Basic password for `/ops` |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Browser base for `/signup`, `/setup` snippets, Stripe redirects |
| `TRIAL_BUDGET_TOKENS` | `200000` | Redis seed for `budget:{org}:default` on signup |
| `RESERVATION_TTL_SEC` | `300` | Unsettled hold expiry |
| `DEFAULT_RESERVATION_ESTIMATE` | `4096` | Fallback when `max_tokens` absent |
| `PROMPT_TOKEN_BUFFER` | `512` | Added to parsed `max_tokens` |
| `PRECHECK_TIMEOUT_MS` | `50` | Redis timeout before fail-open |
| `SLACK_WEBHOOK_URL` | — | Global Slack fallback |
| `STRIPE_SECRET_KEY` | — | Enables Checkout when set with webhook + prices |
| `STRIPE_WEBHOOK_SECRET` | — | `/billing/webhook` signature |
| `STRIPE_PRICE_INDIE` / `STRIPE_PRICE_TEAM` | — | Stripe Price IDs (`indie` also used for `trial`) |
| `STRIPE_SUCCESS_URL` | `{PUBLIC_BASE_URL}/setup?session_id={CHECKOUT_SESSION_ID}` | Must include `{CHECKOUT_SESSION_ID}` |

## Health checks

- `GET /healthz` — liveness (always 200)
- `GET /readyz` — returns 503 when Redis is unreachable (orchestration signal; proxy still serves traffic fail-open)
- `GET /account` — customer HTML (paste `tg_` key; one-shot view)
- `GET /me/buckets`, `/me/usage`, `/me/org` — customer JSON (`X-TokenGuard-Key`)
- `GET /ops` — operator HTML (Basic auth)

## Development

```bash
go test ./... -count=1
go build -o bin/proxy ./cmd/proxy/
```

## Project layout

```
cmd/proxy/          Entry point
internal/
  account/          Customer /me APIs + /account HTML
  admin/            Admin API
  billing/          Stripe Checkout + webhook + provisioning
  budget/           Redis client, Lua scripts, alerts
  config/           Environment-based configuration
  ops/              /ops HTML page
  proxy/            Reverse proxy, enforcement, settlement
  signup/           Public /signup + /setup
  store/            Postgres + memory usage/org stores
  usage/            OpenAI + Anthropic usage extractors
test/integration/   End-to-end tests
```

## License

TBD
