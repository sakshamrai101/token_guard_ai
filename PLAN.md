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
- Budget unit: **raw token count** (integer in Redis; 1:1 with provider `usage.total_tokens`)
- Per-bucket configurable `reservation_estimate` (parsed from `max_tokens` in request body + buffer, or static fallback)

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

**Decisions (locked):**

- Build order: **Phase A non-streaming JSON settle → Phase B SSE streaming → Phase C hardening**
- Provider scope: **OpenAI only** (Anthropic extractor deferred; leave stub or TODO)
- Settlement: `actual = usage.total_tokens` (raw token count, 1:1 with Redis budget unit)
- Streaming settle: **async** (goroutine after final chunk); non-streaming settle: **sync** on body close
- Missing usage / client disconnect: settle at **reserved** amount (worst-case)

**Phase A — Non-streaming JSON settle (do first)**

1. **`internal/usage` package**
   - `Usage` struct: `{PromptTokens, CompletionTokens}` + `Total()` method
   - `UsageExtractor` interface: `ExtractFromJSON(body []byte) (Usage, error)`
   - `openai.go`: parse `usage` from OpenAI chat completion JSON response
   - Unit tests with recorded JSON fixtures

2. **`BudgetSettler` interface** in `internal/proxy` (mirror `BudgetReleaser`)
   - `Settle(ctx, requestID, actual int64) error`
   - Implemented by existing `*budget.Client`

3. **Handler wiring**
   - Store `request_id` + `reserved` amount in request context after pre-check
   - In `modifyResponse` for `StatusCode == 200` and non-SSE `Content-Type`:
     - Wrap `resp.Body` with `settlingReader` that forwards bytes to client
     - On `Close()`/EOF: parse JSON → `ExtractFromJSON` → `Settle(requestID, total)`
   - Wire settler + extractor in `main.go`

4. **Tests (Phase A)**
   - Unit: OpenAI JSON extractor fixtures
   - Integration: mock upstream returns JSON with `usage` → Redis balance reconciled (not held)
   - Integration: actual < reserved → balance refunded via `settle_budget` delta

**Phase B — SSE streaming settle (do second)**

1. **Extend `internal/usage`**
   - `sse/parser.go`: SSE frame state machine (comments, blank lines, `data:` lines, `[DONE]`)
   - `openai_stream.go`: detect `usage` in final `data:` chunk before `[DONE]`
   - `ExtractFromSSE(reader io.Reader) (Usage, error)` or incremental `Feed(line string)`

2. **Stream body tap** in `modifyResponse`
   - When `Content-Type: text/event-stream`: wrap `resp.Body` with `streamTap`
   - `Read()` forwards bytes to client AND feeds SSE parser line-by-line
   - On EOF/`[DONE]`: extract usage → **async** `go settler.Settle(...)` (do not block client)
   - MUST NOT buffer full response body

3. **Tests (Phase B)**
   - Unit: SSE parser with recorded OpenAI stream fixtures
   - Integration: mock upstream SSE stream → balance settled to actual usage after stream ends

**Phase C — Hardening (do third)**

1. **Client disconnect**: watch `resp.Request.Context().Done()` in body wrapper; if canceled before usage → `Settle(requestID, reserved)`
2. **Missing usage**: stream ends without `usage` chunk → `Settle(requestID, reserved)`; increment `usage_missing_total`
3. **Settlement retry**: 3× backoff on Redis `Settle` error; rely on `RESERVATION_TTL` if all fail
4. **Metrics**: add `budget_settle_total`, `usage_missing_total` to `budget/metrics.go`
5. **Structured logs**: extend budget check log with `actual` and `outcome=settled|missing_usage|disconnected`
6. **E2E test**: N concurrent non-streaming requests with known `usage` → final balance correct; request N+1 gets 429
7. **Goroutine leak test**: 1000 aborted streaming requests → `runtime.NumGoroutine()` stable

**Explicitly NOT Day 3:**

- Anthropic SSE (`message_stop`) and JSON usage extractors
- Anthropic `max_tokens` request-body parsing
- gzip `Content-Encoding` decompress tap
- Prometheus `/metrics` HTTP endpoint
- Changes to Lua scripts (`settle_budget` already implemented)
- USD micro-cents cost conversion

**Day 3 Definition of Done:**

- [ ] Non-streaming 200 responses settle budget to actual `usage.total_tokens`
- [ ] Streaming 200 responses settle budget async after final SSE chunk
- [ ] Client disconnect settles at reserved amount
- [ ] Missing usage settles at reserved amount
- [ ] Settlement retry on Redis error (3× backoff)
- [ ] E2E: concurrent requests cannot overspend bucket with real usage settlement
- [ ] No goroutine leaks on 1000 aborted streams
- [ ] All existing Day 1/Day 2 tests still pass

---

## Post-MVP Launch Plan (v1 — Self-Hosted)

**GTM decision (locked):** Ship as a **self-hosted drop-in proxy** first. Clients run proxy + Redis in their own infra. No user database, no managed SaaS, no signup/billing for v1.

**Storage model (locked):** **Redis only** for v1. Budget balances and reservations live in Redis. Postgres/user accounts deferred until a hosted control plane is needed (v2).

**Quality bar:** A devops-minded client can deploy in <30 minutes, prove enforcement works (shadow → enforce), and operate without `redis-cli`.

### Launch-1 — Anthropic Provider Support (Track 2)

**Build order:** Anthropic non-streaming first, then Anthropic SSE streaming. TDD per path. Run `go test ./...` between each.

1. **`internal/usage/anthropic.go`**
   - Non-streaming: parse `usage.input_tokens + usage.output_tokens` from Messages API JSON response
   - `actual = input_tokens + output_tokens`

2. **`internal/usage/anthropic_stream.go`**
   - SSE: extract usage from `message_delta` and/or `message_stop` events
   - Reuse existing `sse/parser.go`; Anthropic uses `event:` + `data:` lines

3. **`internal/budget/estimate.go`**
   - Extend request parsing for Anthropic Messages API body (`max_tokens` field)

4. **Multi-provider routing**
   - Select extractor pair by `UPSTREAM_HOST` or env `PROVIDER=openai|anthropic`
   - `api.openai.com` → OpenAI; `api.anthropic.com` → Anthropic
   - Wire in `cmd/proxy/main.go`

5. **Tests**
   - Unit: `testdata/anthropic_completion.json`, `testdata/anthropic_stream.sse`
   - Integration: mock Anthropic upstream (stream + non-stream) → balance settled

**NOT Launch-1:** Gemini, gzip decompress tap

### Launch-2 — Production Ops (Track 1)

1. **Admin API** (`internal/admin/`)
   - `GET /admin/v1/buckets/{id}` → `{bucket_id, balance}`
   - `PUT /admin/v1/buckets/{id}` → set balance `{"balance": N}`
   - `POST /admin/v1/buckets/{id}/topup` → add tokens `{"amount": N}`
   - Auth: `Authorization: Bearer $ADMIN_API_KEY`
   - Add `set_budget.lua` for atomic admin writes (keeps Lua-only mutation rule)
   - Rate-limit admin routes; register before catch-all `/` in `server.go`

2. **Docker Compose** — `docker-compose.yml`, `Dockerfile`, `.env.example`

3. **Docs** — update `README.md`; `docs/RUNBOOK.md` and `ONBOARDING.md` (in repo)

4. **Smoke test checklist** in RUNBOOK (OpenAI + Anthropic, manual)

### Launch-3 — Launch Essentials

- [ ] `ADMIN_API_KEY` required for `/admin/*`; 401 if invalid
- [ ] Admin routes never proxied upstream
- [ ] Security doc: gateway-injected bucket IDs only
- [ ] Docs default to `ENFORCEMENT_MODE=shadow` for first 48h
- [ ] LICENSE file

### OUT OF v1 Launch

User DB, Postgres, Stripe, dashboard, managed SaaS, Gemini, Prometheus endpoint

### v1 Launch Definition of Done

- [ ] OpenAI + Anthropic stream/non-stream settle correctly
- [ ] Admin API get/set/topup without redis-cli
- [ ] Docker Compose works
- [ ] README + RUNBOOK + ONBOARDING complete
- [ ] `go test ./...` passes

### Client Onboarding

See [ONBOARDING.md](ONBOARDING.md) for full setup walkthrough.

---

## Resolved Design Decisions

| Decision | Choice | Notes |
|----------|--------|-------|
| Budget unit | Raw token count | Integer in Redis; 1:1 with provider `usage` metadata |
| Bucket identity | `X-Budget-Bucket-Id` header | Gateway-injected; already in Day 1 scaffold |
| Reservation estimate | Parse `max_tokens` from request JSON | Fallback to `DEFAULT_RESERVATION_ESTIMATE` (4096) |
| Day 2 settlement | Reserve + release on errors only | 200 responses hold until TTL; Day 3 settles with real usage |
| Day 3 build order | Non-streaming → streaming → hardening | Incremental phases; TDD per phase |
| Day 3 provider scope | OpenAI only | Anthropic deferred post-MVP |
| GTM / launch model | Self-hosted first | No user DB; Redis only for v1 |
| v1 providers | OpenAI + Anthropic | Gemini deferred |
| Budget storage | Redis only (v1) | Postgres when hosted SaaS (v2) |

---

## Definition of Done (MVP)

- [x] Proxy forwards OpenAI chat requests transparently (stream + non-stream) — Day 1
- [x] Concurrent requests respect budget via reservation — Day 2 miniredis test
- [x] Redis outage → requests still forwarded (fail-open) with alert fired — Day 2
- [x] Budget exhausted → 429 without upstream call — Day 2
- [ ] Usage settlement adjusts balance within 1s of stream completion (p99) — Day 3 Phase B
- [ ] No goroutine leaks on 1000 aborted streams (load test) — Day 3 Phase C
- [ ] E2E concurrent overspend prevention with real usage settlement — Day 3 Phase C
