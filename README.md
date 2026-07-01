# Token Guard AI

A drop-in LLM API proxy that enforces per-bucket token budgets in real time. Think of it as a **circuit breaker for your OpenAI/Anthropic API key** — block runaway spend before it hits your invoice, without taking down your app when Redis hiccups.

Built for indie devs and small AI startups who want set-and-forget abuse protection.

## What it does today

- Transparent reverse proxy to OpenAI-compatible APIs (method, path, body unchanged)
- **Pre-request budget reservation** via atomic Redis Lua scripts
- **429** when a bucket can't cover the estimated cost (`enforce` mode)
- **Fail-open** when Redis is unreachable — LLM traffic still flows
- **Release** (full refund) on upstream 4xx/5xx
- Parses `max_tokens` from the request body to estimate reservation size

## What's next

- Parse `usage` from provider responses (streaming + non-streaming)
- Wire `settle_budget` on successful 200 responses
- Accurate balance reconciliation after each completion

See [PLAN.md](PLAN.md) and [ARCHITECTURE.md](ARCHITECTURE.md) for the full roadmap and design rationale.

## Quick start

**Requirements:** Go 1.22+, Redis 7+

```bash
# Start Redis
docker run --rm -p 6379:6379 redis:7

# Seed a bucket (balance in raw token count)
redis-cli SET budget:my-app 50000

# Run the proxy
ENFORCEMENT_MODE=enforce \
REDIS_URL=redis://localhost:6379 \
go run ./cmd/proxy/
```

Point your app at `http://localhost:8080` instead of `api.openai.com`. Add headers:

| Header | Purpose |
|--------|---------|
| `X-Budget-Bucket-Id` | Bucket to charge (gateway-injected in prod) |
| `X-Request-Id` | Idempotency key (auto-generated UUID if omitted) |

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Budget-Bucket-Id: my-app" \
  -d '{"model":"gpt-4o","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}'
```

## Enforcement modes

| Mode | Behavior |
|------|----------|
| `off` | Proxy only, no budget checks (default) |
| `shadow` | Reserve + log, never block |
| `enforce` | Block with 429 when budget exhausted |

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | Proxy listen address |
| `UPSTREAM_URL` | `https://api.openai.com` | Provider base URL |
| `UPSTREAM_HOST` | `api.openai.com` | Host header rewrite target |
| `ENFORCEMENT_MODE` | `off` | `off`, `shadow`, or `enforce` |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection URL |
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
# Run all tests
go test ./... -count=1

# Verbose
go test ./... -count=1 -v

# Build
go build -o bin/proxy ./cmd/proxy/
```

## Project layout

```
cmd/proxy/          Entry point
internal/
  config/           Environment-based configuration
  proxy/            Reverse proxy, enforcement, headers
  budget/           Redis client, Lua scripts, estimate parsing
test/integration/   End-to-end tests (miniredis + mock upstream)
```

## License

TBD
