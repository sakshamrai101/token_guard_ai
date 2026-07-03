# Token Guard AI

A drop-in LLM API proxy that enforces per-bucket token budgets in real time. Think of it as a **circuit breaker for your OpenAI/Anthropic API key** — block runaway spend before it hits your invoice, without taking down your app when Redis hiccups.

Built for indie devs and small AI startups who want set-and-forget abuse protection.

## What it does

- Transparent reverse proxy to **OpenAI** or **Anthropic** (method, path, body unchanged)
- **Pre-request budget reservation** via atomic Redis Lua scripts
- **Post-response settlement** from provider usage metadata (stream + non-stream)
- **429** when a bucket can't cover the estimated cost (`enforce` mode)
- **Fail-open** when Redis is unreachable — LLM traffic still flows
- **Release** (full refund) on upstream 4xx/5xx
- **Admin API** for bucket balance get/set/topup (no `redis-cli`)
- Parses `max_tokens` from the request body to estimate reservation size

Provider routing is by `UPSTREAM_HOST`: `api.openai.com` → OpenAI extractors; `api.anthropic.com` → Anthropic extractors. Run one proxy instance per provider.

See [PLAN.md](PLAN.md), [ARCHITECTURE.md](ARCHITECTURE.md), [ONBOARDING.md](ONBOARDING.md), and [docs/RUNBOOK.md](docs/RUNBOOK.md) for rollout and ops.

## Docker quick start

**Requirements:** Docker + Docker Compose

```bash
cp .env.example .env
# Edit .env — set ADMIN_API_KEY to a long random secret

docker compose up -d --build

curl http://localhost:8080/healthz   # {"status":"ok"}
curl http://localhost:8080/readyz    # {"status":"ready"}
```

### Seed a bucket (admin API)

```bash
export ADMIN_API_KEY=change-me-to-a-long-random-secret   # match .env

curl -X PUT http://localhost:8080/admin/v1/buckets/my-app \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"balance": 50000}'

curl http://localhost:8080/admin/v1/buckets/my-app \
  -H "Authorization: Bearer $ADMIN_API_KEY"
# {"bucket_id":"my-app","balance":50000}
```

### Proxy a request

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Budget-Bucket-Id: my-app" \
  -d '{"model":"gpt-4o","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}'
```

After the response completes, the bucket balance is reconciled to actual `usage.total_tokens` (OpenAI) or `input_tokens + output_tokens` (Anthropic).

## Local development (without Docker)

**Requirements:** Go 1.22+, Redis 7+

```bash
docker run --rm -p 6379:6379 redis:7

export ADMIN_API_KEY=dev-secret
export ENFORCEMENT_MODE=shadow
export REDIS_URL=redis://localhost:6379

go run ./cmd/proxy/
```

## Headers

| Header | Purpose |
|--------|---------|
| `X-Budget-Bucket-Id` | Bucket to charge (gateway-injected in prod) |
| `X-Request-Id` | Idempotency key (auto-generated UUID if omitted) |

Do not trust client-supplied bucket IDs in production — inject at your gateway.

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
| GET | `/admin/v1/buckets/{id}` | — |
| PUT | `/admin/v1/buckets/{id}` | `{"balance": N}` |
| POST | `/admin/v1/buckets/{id}/topup` | `{"amount": N}` |

```bash
# Top up
curl -X POST http://localhost:8080/admin/v1/buckets/my-app/topup \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"amount": 10000}'
```

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
| `ADMIN_API_KEY` | — | Bearer token for `/admin/*` |
| `RESERVATION_TTL_SEC` | `300` | Unsettled hold expiry |
| `DEFAULT_RESERVATION_ESTIMATE` | `4096` | Fallback when `max_tokens` absent |
| `PROMPT_TOKEN_BUFFER` | `512` | Added to parsed `max_tokens` |
| `PRECHECK_TIMEOUT_MS` | `50` | Redis timeout before fail-open |
| `SLACK_WEBHOOK_URL` | — | Optional alerts on fail-open / deny |

## Health checks

- `GET /healthz` — liveness (always 200)
- `GET /readyz` — returns 503 when Redis is unreachable (orchestration signal; proxy still serves traffic fail-open)

## Development

```bash
go test ./... -count=1
go build -o bin/proxy ./cmd/proxy/
```

## Project layout

```
cmd/proxy/          Entry point
internal/
  admin/            Admin API (bucket get/set/topup)
  budget/           Redis client, Lua scripts, estimate parsing
  config/           Environment-based configuration
  proxy/            Reverse proxy, enforcement, settlement taps
  usage/            OpenAI + Anthropic usage extractors
test/integration/   End-to-end tests (miniredis + mock upstream)
```

## License

TBD
