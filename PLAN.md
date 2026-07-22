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

## Hosted Product v1 (Lean Multi-Tenant)

**GTM (locked):** You operate **one multi-tenant TokenGuard instance** on a VPS. Customers pay via Stripe → get a TokenGuard API key → point their SDK `base_url` at your proxy. Codebase remains deployable via Docker; you are the operator.

**Quality bar:** Setup in minutes (change base_url + key). Slack when budgets warn/exhaust. Usage dump + minimal ops page. No React dashboard. Learn from demand before building more.

**Pricing (locked for v1):**

| Plan | Price | Limits |
|------|-------|--------|
| Trial | Free 14 days | 1 bucket, 200k tokens proxied |
| Indie | **$15/mo** | 5 buckets, 5M tokens/mo, Slack |
| Team | **$39/mo** | 25 buckets, 25M tokens/mo, Slack + usage dump |

**Locked technical decisions:**

| Item | Choice |
|------|--------|
| TokenGuard auth header | `X-TokenGuard-Key: tg_...` (never overload provider Bearer) |
| Redis budget key | `budget:{org_id}:{bucket_id}` |
| Hot path | Redis Lua (unchanged) |
| Durable state | Postgres: orgs, api_keys, usage_events, Stripe fields, slack_webhook_url |
| Provider keys | Passthrough only — never store |
| UX in v1 | Dump APIs + `/ops` HTML — **no React SPA** |

### Explicitly OUT of Hosted v1

- React/SPA dashboard
- Gemini / Grok native extractors
- Interactive Slack buttons
- Email notifications
- Fancy charts / analytics
- Holding customer provider API keys
- Per-customer self-hosted Docker as the paid SKU

### Hosted v1 phase status

| Phase | Status |
|-------|--------|
| **H1** Usage log + dump APIs | **Done** |
| **H2** Multi-tenant auth (`tg_` keys, org-scoped Redis) | **Done** |
| **H3** Slack per-org + 80% warning | **Done** |
| **H4** Stripe Checkout + webhook | **Done** |
| **H5** Minimal `/ops` HTML page | **Done** |
| **H6** Postgres in Compose + VPS deploy docs | **Done** |

Do NOT combine phases. TDD each phase. `go test ./...` must stay green. Update [ARCHITECTURE.md](ARCHITECTURE.md) **after** each phase ships (not before).

---

### H1 — Usage log + dump APIs (DONE)

**Shipped:**

- `internal/store` — `UsageEvent`, memory + Postgres stores; `org_id=default` until H2
- Settle/release → `usage_events` (`settled` / `missing_usage` / `disconnected` / `released`)
- Admin dump APIs: `GET /admin/v1/usage`, `GET /admin/v1/buckets`, `GET /admin/v1/reservations`
- `DATABASE_URL` optional (memory store when unset)

---

### H2 — Multi-tenant auth (DONE)

**Shipped:**

- Postgres: `orgs`, `api_keys` (hash + prefix), `buckets` registry
- Auth middleware: `X-TokenGuard-Key` → org; missing/invalid → **401**
- Redis keys: `budget:{org_id}:{bucket_id}`
- Admin: create org, create key (raw `tg_` returned once)
- Usage events use real `org_id` from context

---

### H3 — Slack per-org + 80% warning (DONE)

**Shipped:**

- `slack_webhook_url` on orgs + admin PATCH
- Org webhook preferred over global `SLACK_WEBHOOK_URL`
- Alerts: `budget_exhausted`, `budget_warning_80` (1h Redis dedupe), `fail_open`

---

### H4 — Stripe Checkout + webhook (DONE)

**Shipped:**

- `internal/billing` — Checkout Session + webhook verify
- `POST /admin/v1/orgs/{id}/checkout`, `POST /billing/webhook`
- Org plan `trial` | `indie` | `team` + Stripe customer/subscription IDs

---

### H5 — Minimal `/ops` HTML page (DONE)

**Shipped:**

- `GET /ops` — Go `html/template` + minimal CSS
- HTTP Basic auth (`admin` / `ADMIN_API_KEY`)
- Buckets (with org_id), last 50 usage events, open reservations
- Mounted on server mux (not proxied)

**H5 Definition of Done:**

- [x] `/ops` usable in browser with Basic auth
- [x] Shows balances + usage + reservations
- [x] `go test ./...` green

---

### H6 — Deploy (Compose Postgres + VPS docs) (DONE)

**Shipped:**

- `postgres` in `docker-compose.yml` (healthcheck + `pgdata` volume); proxy waits on healthy redis + postgres
- `.env.example` documents `DATABASE_URL`, Stripe, Slack, admin/redis
- Schema auto-migrate on startup (`usage_events`, orgs/keys/buckets, Slack + Stripe columns)
- `docs/RUNBOOK.md` — Compose smoke, VPS outline, org → key → Slack → Checkout → `/ops`
- `README.md` — hosted quickstart with `X-TokenGuard-Key`

**H6 Definition of Done:**

- [x] Compose runs proxy + redis + postgres
- [x] RUNBOOK + README updated for hosted multi-tenant
- [x] Hosted v1 overall DoD checklist below is checkable

---

### Hosted v1 overall Definition of Done

- [x] Settled/released requests appear in usage dump (H1)
- [x] Customer with `tg_` key can call proxy; unknown key → 401 (H2)
- [x] Budget keys scoped per org in Redis (H2)
- [x] Slack fires on exhaust + 80% + fail-open (H3)
- [x] Stripe test-mode Checkout activates Indie/Team plan (H4)
- [x] `/ops` shows balances + recent requests (admin auth) (H5)
- [x] `docker compose up` runs proxy + redis + postgres (H6)
- [x] `go test ./...` green

---

### Post–Hosted-v1 (build on demand)

| Priority | Feature | Notes |
|----------|---------|-------|
| **S1** | Self-serve signup → Stripe → setup page with one-time key | **Done** — branch `self-serve` |
| **A1** | Customer analytics: `/me` + `/account` | **Done** — branch `analytics` |
| **M1 (NOW)** | One-app multi-provider routing (OpenAI + Anthropic) | See **M1** below — **branch `multi-routing`** |
| **P1** | Gemini native JSON + SSE extractors | After M1 |
| **P1** | OpenAI-compatible upstream picker (Grok / custom base URL) | After M1 |
| **P2** | Real dashboard (React) | On demand — A1 is the lean stand-in |
| **P2** | Slack interactive top-up / email alerts | On demand |
| **P3** | SSO, Prometheus, community self-hosted docs | Later |

---

## M1 — Multi-Provider Routing (OpenAI + Anthropic, one deploy)

**Branch:** `multi-routing`  
**Goal:** A single Token Guard hostname/process can budget-protect apps that call **both** OpenAI and Anthropic — same `tg_` key, same org/buckets, correct upstream + usage extractor per request.

**Why now:** Many production apps use both providers. Today one `UPSTREAM_URL` per process forces two deploys or leaves half their traffic unguarded.

**Quality bar:** Customer points OpenAI SDK at `…/openai/v1` and Anthropic SDK at `…/anthropic` (documented paths). Both reserve/settle against the same org Redis buckets. Legacy single-upstream `/v1/…` still works.

### Locked decisions

| Item | Choice |
|------|--------|
| Routing key | **URL path prefix** (not Host header, not body sniffing) |
| OpenAI prefix | `/openai/` → strip prefix → forward to OpenAI upstream (`/openai/v1/chat/completions` → `/v1/chat/completions`) |
| Anthropic prefix | `/anthropic/` → strip prefix → forward to Anthropic upstream (`/anthropic/v1/messages` → `/v1/messages`) |
| Legacy path | Unprefixed proxy paths continue to use existing `UPSTREAM_URL` / `UPSTREAM_HOST` + `RegistryForHost` — **backward compatible** |
| Extractors | **Per-request** — path selects OpenAI vs Anthropic JSON/SSE extractors |
| Auth / budgets | Unchanged: `X-TokenGuard-Key`, `budget:{org_id}:{bucket_id}`, default bucket |
| Config | `OPENAI_UPSTREAM_URL` / `OPENAI_UPSTREAM_HOST` and `ANTHROPIC_UPSTREAM_URL` / `ANTHROPIC_UPSTREAM_HOST` (defaults to api.openai.com / api.anthropic.com). Multi prefixes enabled when both provider URL pairs are configured (non-empty); always keep legacy `UPSTREAM_*` |
| Gemini / Grok | **OUT of M1** |

### Deliverables (single changeset — do it all)

1. **Config + `.env.example`** — OpenAI + Anthropic upstream URL/host pairs; keep legacy `UPSTREAM_*`
2. **Per-request provider on proxy handler** — detect from path; context carries upstream + extractors; `Director` rewrites host/path (strip prefix); settle uses selected extractors
3. **Do not route** `/admin`, `/me`, `/account`, `/ops`, `/signup`, `/setup`, `/billing`, `/healthz`, `/readyz` through provider prefixes
4. **Same budget identity** — OpenAI + Anthropic calls share org/bucket; integration test both settles decrement same balance
5. **Docs** — README + ONBOARDING dual `base_url` examples; RUNBOOK multi vs single mode
6. **Optional** — light `/setup` snippet update showing both base URLs if low-risk

### Explicitly OUT of M1

- Gemini, Grok, arbitrary compatible-base picker UI
- Body-based provider detection
- Separate Redis budgets per provider
- Breaking removal of legacy single `UPSTREAM_URL` mode
- React dashboard / auth / Stripe changes

### M1 Definition of Done

- [x] `/openai/v1/…` → OpenAI upstream + OpenAI extractors + settle
- [x] `/anthropic/v1/…` → Anthropic upstream + Anthropic extractors + settle
- [x] Same `tg_` key + bucket works for both; balance reflects both
- [x] Legacy unprefixed path still uses `UPSTREAM_*` (regression)
- [x] Stream + non-stream covered per provider (integration tests)
- [x] App routes (admin/me/account/signup/…) unchanged
- [x] Existing tests green; new routing tests; `go test ./...` green

### Tests (required)

- Path strip + Host rewrite unit tests (both prefixes)
- Integration: mock OpenAI on `/openai/…` → OpenAI usage settle
- Integration: mock Anthropic on `/anthropic/…` → Anthropic usage settle
- Integration: OpenAI then Anthropic same bucket → correct final balance
- Legacy `/v1/…` still hits `UPSTREAM_*` mock
- Reserved app routes not captured by provider prefix logic

---

## A1 — Customer Analytics (self-serve visibility)

**Branch:** `analytics` (**done**)  
**Goal:** Paying customers can inspect **their** bucket balances and recent usage **without** Slack-only alerts and **without** emailing the operator. Slack remains the push channel; `/account` + `/me` are the investigation surface.

**Why now:** S1 gets them a key. Without org-scoped visibility, every “what’s my balance?” becomes a support ticket — weak for $15/mo.

**Quality bar:** Customer with `tg_` key opens `/account`, pastes key (or uses header), sees org buckets + last N usage events + can update Slack webhook. Operator `/ops` (ADMIN_API_KEY) unchanged for support.

### Locked decisions

| Item | Choice |
|------|--------|
| Auth | Customer routes use `X-TokenGuard-Key` (same as LLM proxy) — **not** `ADMIN_API_KEY` |
| Scope | Data filtered to **that org only** — never cross-tenant |
| APIs | Read-focused first: balances + usage (+ optional Slack PATCH) |
| UI | Minimal Go `html/template` page `/account` — **no React** |
| Topup / remint key | **OUT of A1** (support / admin mint remains) |
| Multi-provider routing | **OUT of A1** — separate later branch |
| Charts / CSV export / billing portal | **OUT of A1** |

### Deliverables (single changeset — do it all)

1. **Customer APIs** (package e.g. `internal/account` or extend `internal/ops` with customer handlers — prefer `internal/account`)
   - `GET /me/buckets` → `{buckets:[{bucket_id, balance}, ...]}` for authenticated org
   - `GET /me/usage?limit=50` → recent `usage_events` for that org only (newest first)
   - `GET /me/org` → `{org_id, plan, default_bucket_id, slack_webhook_url_set: bool}` (do **not** echo full webhook secret if avoidable — or mask)
   - `PATCH /me/slack` → `{slack_webhook_url}` update org webhook (same validation as setup)
2. **Auth middleware reuse**
   - Resolve `X-TokenGuard-Key` → org (reuse `proxy.AuthMiddleware` pattern or shared lookup)
   - Missing/invalid key → **401** JSON
3. **Minimal `/account` HTML**
   - `GET /account` — form or prompt for key **or** accept key via header from a small paste UI (session-less: paste `tg_` once per visit into a form that POSTs to same origin and re-renders — **no long-lived cookie store of raw key in A1** unless trivial; prefer: page explains “send requests with header” for API users + HTML form that POSTs key to `POST /account/view` which renders balances for that request only)
   - Show: plan, default bucket, table of bucket balances, table of last 50 usage events, Slack webhook update form
   - Plain CSS consistent with `/ops` / `/setup` (readable, mobile-ok)
4. **Wire in `main.go` / server mux**
   - Mount `/me/*` and `/account` when org store + Redis available (hosted mode)
   - Do **not** proxy these paths upstream
5. **Docs**
   - README + ONBOARDING: “Check balances at `/account` or `GET /me/buckets`”
   - RUNBOOK: customer self-serve vs operator `/ops`

### Explicitly OUT of A1

- React/SPA dashboard
- Stripe Customer Portal / invoices UI
- Self-serve bucket topup or balance set
- Key remint / revoke UI
- Email alerts
- Multi-provider path routing (OpenAI+Anthropic one hostname)
- Gemini / Grok
- Hard plan quota enforcement UI

### A1 Definition of Done

- [x] `GET /me/buckets` with valid `tg_` key returns only that org’s balances
- [x] `GET /me/usage` scoped to org; other orgs’ events never appear
- [x] Invalid/missing key → 401
- [x] `GET /account` (or view flow) shows balances + recent usage for a pasted/submitted key
- [x] Customer can PATCH Slack webhook from `/me/slack` or account form
- [x] Operator `/ops` and admin dump APIs still work unchanged
- [x] Existing H1–H6 + S1 tests still pass; new unit/integration tests for `/me` scoping
- [x] `go test ./...` green

### Tests (required)

- `/me/buckets` 401 without key; 200 with key; empty org → empty list
- Two orgs: org A key never sees org B balances or usage
- `/me/usage` limit honored; ordered newest first
- `/me/slack` updates org webhook
- `/account` HTML renders for valid key path; rejects invalid
- Admin `/ops` still Basic-auth only (regression)

---

## S1 — Self-Serve Onboarding (FINISHED PRODUCT FEEL)

**Branch:** `self-serve` (**done**)  
**Goal:** Customer never needs the operator after landing page. Signup → pay (or trial Checkout) → see `tg_` key once → copy SDK snippet → call proxy. Feels like a finished product.

**Quality bar:** Landing “Get started” completes without admin curl. Operator `/ops` remains for support only.

### Locked decisions

| Item | Choice |
|------|--------|
| Entry | Public `POST /signup/checkout` (email + plan) and/or simple `GET /signup` HTML form |
| Payment | Existing Stripe Checkout (Indie/Team); trial can use Checkout with trial period **or** free path that still creates org+key with seeded budget (document which — prefer Stripe Checkout with `trial` plan metadata for one code path) |
| After payment | Webhook creates org (from email), mints `tg_` key, upserts bucket `default`, seeds Redis balance, stores one-time key blob |
| Key reveal | `GET /setup?session_id=cs_...` HTML — shows key **once**, then deletes one-time secret |
| Default bucket | Wire `default_bucket_id=default`; if `X-Budget-Bucket-Id` missing, use `default` |
| Trial seed | Env `TRIAL_BUDGET_TOKENS` (default 200000) |
| Slack | Optional field on `/setup` page → PATCH org webhook |
| Auth header | Still `X-TokenGuard-Key` |

### Deliverables (single changeset — do it all)

1. **Public signup**
   - `GET /signup` — minimal HTML: email, plan select (trial/indie/team), submit
   - `POST /signup/checkout` — JSON `{email, plan}` → Stripe Checkout Session URL (metadata: email, plan); no `ADMIN_API_KEY`
2. **Webhook enrichment** (`checkout.session.completed`)
   - If org for email missing → create org (name=email or derived)
   - Mint API key; persist hash
   - Upsert `buckets (org_id, default)` and set `orgs.default_bucket_id = default`
   - Seed Redis `budget:{org_id}:default` = `TRIAL_BUDGET_TOKENS` (or plan-based seed)
   - Store one-time plaintext key in Redis `setup:session:{checkout_session_id}` TTL 15m (or Postgres table)
   - Set Stripe IDs + plan as today
3. **Setup page**
   - `GET /setup?session_id=` — HTML: shows `tg_` key once, proxy base URL, copy-paste Python + curl snippets using `default` bucket
   - Optional Slack webhook input → save on org
   - Second visit / expired → “key already revealed or expired — contact support” (no re-show plaintext)
4. **Default bucket in proxy**
   - Empty `X-Budget-Bucket-Id` → use org `default_bucket_id` or `"default"` (do **not** fail-open for missing bucket when default exists)
5. **Config / compose**
   - `TRIAL_BUDGET_TOKENS`, `PUBLIC_BASE_URL` (for snippets and Stripe success URL → `/setup?session_id={CHECKOUT_SESSION_ID}`)
   - Stripe success URL must include `{CHECKOUT_SESSION_ID}` template
6. **Docs**
   - README + ONBOARDING + RUNBOOK: self-serve path is primary; admin mint is fallback for support

### Explicitly OUT of S1

- React dashboard, magic-link login later, email sending key (page reveal only), Gemini, team invites, hard plan quota enforcement

### S1 Definition of Done

- [x] Unauthenticated user can start Checkout from `/signup`
- [x] After test payment / webhook: org + key + `default` bucket + Redis seed exist
- [x] `/setup?session_id=` shows key once; second load does not
- [x] LLM call with only `X-TokenGuard-Key` (no bucket header) uses `default` bucket and settles
- [x] Slack optional save from setup page works
- [x] Existing H1–H6 tests still pass; new tests for signup/webhook/setup/default-bucket
- [x] `go test ./...` green

### Tests (required)

- Signup checkout returns URL (mocked Stripe)
- Webhook creates org+key+seed (miniredis + store)
- Setup reveals once then empties
- Proxy default bucket when header absent
- Invalid/expired session_id → clear error page/JSON

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
| Engine launch (Docker) | Self-hosted Redis + admin API | Complete on `production` branch |
| **Paid product v1** | **You host one multi-tenant instance** | Stripe $15/$39; customers get `tg_` keys |
| Hosted v1 storage | Redis (budgets) + Postgres (orgs, keys, usage, Stripe) | No React dashboard |
| Hosted v1 UX | Ops HTML page + dump APIs + Slack | Dashboard post-v1 on demand |
| Provider keys | Customer passthrough | TokenGuard does not store provider secrets |
| **S1 onboarding** | **Self-serve Checkout → `/setup` one-time key** | Operator mint is support fallback only |
| **S1 default bucket** | **`default` auto-seeded; header optional** | Missing header uses org default — not fail-open |
| **A1 customer visibility** | **`/me` APIs + `/account` HTML (tg_ auth)** | Slack = alerts; account = investigate; no React |
| **M1 multi-provider** | **Path prefixes `/openai/` + `/anthropic/` on one process** | Same tg_ key + buckets; legacy `UPSTREAM_*` kept |

---

## Definition of Done (MVP)

- [x] Proxy forwards OpenAI chat requests transparently (stream + non-stream) — Day 1
- [x] Concurrent requests respect budget via reservation — Day 2 miniredis test
- [x] Redis outage → requests still forwarded (fail-open) with alert fired — Day 2
- [x] Budget exhausted → 429 without upstream call — Day 2
- [ ] Usage settlement adjusts balance within 1s of stream completion (p99) — Day 3 Phase B
- [ ] No goroutine leaks on 1000 aborted streams (load test) — Day 3 Phase C
- [ ] E2E concurrent overspend prevention with real usage settlement — Day 3 Phase C
