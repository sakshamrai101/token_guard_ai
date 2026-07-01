# AI Token Budgeter — MVP Plan

## Problem Statement

An AI budget enforcement proxy that sits between application and LLM provider APIs, preventing runaway spend by enforcing per-bucket token/cost budgets in real time. It functions as an intelligent circuit breaker: block requests when budget is exhausted, track actual usage from provider responses, and **never take down production LLM traffic** when the enforcement layer itself is degraded.

**Non-goals (MVP):** Multi-provider cost normalization dashboard, billing/invoicing, client SDK, WebSocket/gRPC protocols.

For engineering constraints and standards, see [.cursorrules](.cursorrules). For technical rationale and design decisions, see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## Core Functional Requirements

### 1. Transparent Proxying

- Forward method, path, query string, and body unchanged
- Rewrite `Host` to provider hostname; strip hop-by-hop headers
- Do **not** forward internal headers (`X-Budget-Bucket-Id`, etc.)
- Support TLS termination at proxy; upstream TLS to provider
- Connection pooling via tuned `http.Transport` (`MaxIdleConns`, `MaxIdleConnsPerHost`, `IdleConnTimeout`)

### 2. Budget Buckets

- Bucket identity from trusted header `X-Budget-Bucket-Id` (MVP: trusted network / gateway injects it)
- Budget unit: **USD micro-cents** (integer) OR **token count** — pick at implementation start; store as integer in Redis
- Per-bucket configurable `reservation_estimate` (default from model + `max_tokens` in request body, or static fallback)

### 3. Circuit Breaker (Pre-Request)

- Run `reserve_budget` Lua before upstream forward
- If `balance < reservation_estimate` in enforce mode: return 429 immediately
- If reservation succeeds: forward request

### 4. Usage Tracking

**Streaming (SSE):**

- Tee response bytes to client without buffering full body
- Parse SSE frames with provider-specific extractor:
  - OpenAI: `usage` field in final data chunk(s)
  - Anthropic: usage in `message_delta` / `message_stop` events
- Extract `{prompt_tokens, completion_tokens}` → convert to budget units → async `settle_budget`

**Non-streaming (required for MVP):**

- Wrap response body; parse JSON `usage` field on complete response
- Settle before closing response to client (sync acceptable — no stream blocking concern)

**Client disconnect:**

- Detect `Context.Done()` / write error
- Settle at reserved amount if usage not yet received

### 5. Idempotency

- Require `X-Request-Id` header (generate UUID if absent)
- Lua scripts detect duplicate `request_id`: return existing reservation, do not double-hold

---

## MVP Roadmap (72-Hour Sprint)

### Day 1 — Proxy Scaffold

- Go module, `cmd/proxy/main.go`
- Transparent pass-through for OpenAI chat completions (streaming + non-streaming)
- `http.Transport` connection pooling
- Header sanitization (Host rewrite, hop-by-hop strip)
- `ENFORCEMENT_MODE=off`
- `/healthz`, `/readyz`
- Integration test: proxy forwards to mock upstream

### Day 2 — Redis Budget Engine

**Decisions (locked):**

- Budget unit: **raw token count** (integer in Redis; maps 1:1 to provider `usage` in Day 3)
- Reservation estimate: **parse `max_tokens` from request JSON body** (fallback to `DEFAULT_RESERVATION_ESTIMATE` env, e.g. 4096)
- Settlement in Day 2: **reserve + release on upstream errors only**; successful 200 responses hold reservation until TTL — real `settle_budget` with usage metadata is Day 3

**Deliverables:**

1. **`internal/budget` package**
   - `go-redis` client with pooled connections (`PoolSize` ≥ 10, `MinIdleConns` ≥ 10, 50ms command timeout)
   - Embed Lua scripts; `SCRIPT LOAD` at startup; `EVALSHA` in hot path
   - `RedisBudgetChecker` implementing `proxy.BudgetChecker` (`Reserve`)
   - `Release(ctx, requestID)` for upstream 4xx/5xx (refund full hold)
   - `Settle(ctx, requestID, actual)` — implement + test in miniredis, **not wired on 200 responses yet**

2. **Lua scripts** (`internal/budget/lua/`)
   - `reserve_budget` — atomic hold; idempotent on `request_id`
   - `settle_budget` — reconcile actual vs reserved (Day 3 wiring)
   - `release_budget` — full refund of hold on upstream error (Day 2 wiring)

3. **Estimate extraction** (`internal/budget/estimate.go`)
   - Peek/parse JSON request body for `max_tokens` (OpenAI chat completions shape)
   - Add prompt-token buffer constant (e.g. `+512`) or env `PROMPT_TOKEN_BUFFER`
   - If body unreadable or `max_tokens` absent → `DEFAULT_RESERVATION_ESTIMATE`

4. **Wire into existing scaffold**
   - `cmd/proxy/main.go`: construct `RedisBudgetChecker`, pass to `NewEnforcement`; Redis `ReadinessChecker` to `NewServer`
   - `handler.go`: replace `estimate=0` with parsed estimate; call `Release` in `ModifyResponse` when `resp.StatusCode >= 400`
   - Generate `X-Request-Id` UUID when header absent (needed for reservation keys)

5. **Observability**
   - `budget_check_total{result=allowed|denied|fail_open}`
   - `budget_reserve_duration_seconds` histogram
   - `fail_open_total` counter (increment in handler when `result.FailOpen`)
   - Structured log per request: `{request_id, bucket_id, reserved, mode, outcome}`

6. **Alerts**
   - Slack webhook on fail-open (CRITICAL) and budget denied (WARN)
   - Env: `SLACK_WEBHOOK_URL` (optional — log-only if unset)

7. **Config additions**
   - `REDIS_URL` (required when mode ≠ off)
   - `RESERVATION_TTL_SEC` (default 300)
   - `DEFAULT_RESERVATION_ESTIMATE` (default 4096)
   - `PROMPT_TOKEN_BUFFER` (default 512)
   - `REDIS_POOL_SIZE`, `REDIS_MIN_IDLE_CONNS`
   - `SLACK_WEBHOOK_URL`

8. **Tests**
   - `miniredis`: reserve idempotency, deny when insufficient, concurrent reserves don't overspend, release refunds, settle reconciles
   - Integration: exhausted bucket → 429 without upstream call (enforce mode)
   - Integration: Redis down → request forwarded (fail-open)
   - Integration: `/readyz` → 503 when Redis unreachable

**Explicitly NOT Day 2:**

- SSE/JSON usage parsing and async settlement on 200 responses (Day 3)
- Concurrent overspend E2E with real usage settlement (Day 3)
- Anthropic request body parsing (Day 3 unless trivial)

### Day 3 — Usage Extraction & Hardening

- SSE parser with OpenAI usage extractor
- Anthropic usage extractor (basic)
- Non-streaming JSON usage settlement
- Client-disconnect settlement
- Idempotency via `X-Request-Id`
- Reservation TTL + expiry cleanup
- End-to-end test: N concurrent requests cannot overspend bucket

---

## Resolved Design Decisions

| Decision | Choice | Notes |
|----------|--------|-------|
| Budget unit | Raw token count | Integer in Redis; 1:1 with provider `usage` metadata |
| Bucket identity | `X-Budget-Bucket-Id` header | Gateway-injected; already in Day 1 scaffold |
| Reservation estimate | Parse `max_tokens` from request JSON | Fallback to `DEFAULT_RESERVATION_ESTIMATE` (4096) |
| Day 2 settlement | Reserve + release on errors only | 200 responses hold until TTL; Day 3 settles with real usage |

---

## Definition of Done (MVP)

- [ ] Proxy forwards OpenAI + Anthropic chat requests transparently (stream + non-stream)
- [ ] Concurrent requests respect budget via reservation (demonstrated by test)
- [ ] Redis outage → requests still forwarded (fail-open) with alert fired
- [ ] Budget exhausted → 429 without upstream call
- [ ] Usage settlement adjusts balance within 1s of stream completion (p99)
- [ ] No goroutine leaks on 1000 aborted streams (load test)
