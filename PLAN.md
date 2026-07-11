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
| **H3** Slack per-org + 80% warning | **Next** |
| **H4** Stripe Checkout + webhook | Pending |
| **H5** Minimal `/ops` HTML page | Pending |
| **H6** Postgres in Compose + VPS deploy docs | Pending |

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

### H3 — Slack per-org + 80% warning (NEXT)

**Goal:** Per-org Slack webhooks for exhausted, 80% warning, and fail-open.

**Deliverables:**

1. Persist `slack_webhook_url` on `orgs`
2. Admin: `PATCH /admin/v1/orgs/{id}` with `{ "slack_webhook_url": "https://hooks.slack.com/..." }`
3. Alert routing: org webhook when set; else global `SLACK_WEBHOOK_URL`
4. **`budget_exhausted`** — on reserve denied in `enforce` (org_id, bucket_id, request_id)
5. **`budget_warning_80`** — after successful settle: if remaining ≤ 20% of (balance + actual) or of configured cap; **dedupe** once per org+bucket per hour (Redis key TTL 1h)
6. **`fail_open`** — keep CRITICAL path; prefer org webhook when org known
7. Extend existing `Alerter` / thin wrapper — do not break current call sites

**Tests:**

- httptest mock: exhausted, warning, fail-open each fire once
- Dedupe: two under-threshold settles within TTL → one warning
- Org webhook preferred over global when both set
- Full suite green

**NOT H3:** Stripe, `/ops`, interactive Slack buttons, email

**H3 Definition of Done:**

- [ ] PATCH org sets Slack webhook
- [ ] Exhausted / 80% / fail-open alerts verified in tests
- [ ] Dedupe works
- [ ] `go test ./...` green

---

### H4 — Stripe Checkout + webhook

**Goal:** Stripe test-mode Checkout activates Indie ($15) / Team ($39); subscription deleted downgrades.

**Deliverables:**

1. Env: `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `STRIPE_PRICE_INDIE`, `STRIPE_PRICE_TEAM`
2. Package `internal/billing` — Checkout Session + webhook signature verify
3. `POST /admin/v1/orgs/{id}/checkout` — body `{ "plan": "indie"|"team" }` → Checkout URL (ADMIN_API_KEY)
4. `POST /billing/webhook` — `checkout.session.completed` → set `orgs.plan` + Stripe IDs; `customer.subscription.deleted` → plan back to `trial`
5. Persist `stripe_customer_id`, `stripe_subscription_id` on org
6. Soft enforce for v1: allow traffic on trial; Slack warn if useful — **no hard paywall required in H4**

**Tests:**

- Webhook with mocked secret updates `org.plan`
- Checkout endpoint returns URL from mocked Stripe client
- Invalid signature → 400
- Full suite green

**NOT H4:** Self-serve signup UI, customer portal, `/ops`

**H4 Definition of Done:**

- [ ] Admin can start Checkout for an org
- [ ] Webhook activates/downgrades plan
- [ ] Document Stripe CLI forward for local test
- [ ] `go test ./...` green

---

### H5 — Minimal `/ops` HTML page

**Goal:** Server-rendered ops view — no React.

**Deliverables:**

1. `GET /ops` — Go `html/template` + minimal CSS
2. Auth: HTTP Basic (user `admin`, password = `ADMIN_API_KEY`) → 401 if wrong
3. Sections: bucket balances (with org_id), last 50 `usage_events`, open reservations
4. Register on mux before catch-all; never proxy `/ops` upstream

**Tests:**

- Unauthorized → 401
- Authorized → 200 + expected headings
- Full suite green

**NOT H5:** Charts, customer dashboard, Stripe UI

**H5 Definition of Done:**

- [ ] `/ops` usable in browser with Basic auth
- [ ] Shows balances + usage + reservations
- [ ] `go test ./...` green

---

### H6 — Deploy (Compose Postgres + VPS docs)

**Goal:** One-command stack + operator docs.

**Deliverables:**

1. `postgres` service in `docker-compose.yml` (healthcheck + volume)
2. `.env.example` — `DATABASE_URL`, Stripe vars, Slack, existing admin/redis
3. Schema auto-migrate on proxy startup (orgs / keys / usage / Stripe columns)
4. `docs/RUNBOOK.md` — VPS deploy, Stripe webhook URL, create org → key → Slack → Checkout
5. `README.md` — hosted quickstart with `X-TokenGuard-Key`

**Tests / gate:**

- `go test ./...` green
- Manual: `docker compose up -d --build` smoke checklist documented

**NOT H6:** K8s, Terraform, CI deploy, Gemini

**H6 Definition of Done:**

- [ ] Compose runs proxy + redis + postgres
- [ ] RUNBOOK + README updated for hosted multi-tenant
- [ ] Hosted v1 overall DoD checklist below is checkable

---

### Hosted v1 overall Definition of Done

- [x] Settled/released requests appear in usage dump (H1)
- [x] Customer with `tg_` key can call proxy; unknown key → 401 (H2)
- [x] Budget keys scoped per org in Redis (H2)
- [ ] Slack fires on exhaust + 80% + fail-open (H3)
- [ ] Stripe test-mode Checkout activates Indie/Team plan (H4)
- [ ] `/ops` shows balances + recent requests (admin auth) (H5)
- [ ] `docker compose up` runs proxy + redis + postgres (H6)
- [ ] `go test ./...` green

---

### Post–Hosted-v1 (build on demand)

Overview of what comes **after** H6 and proven interest — **not in H3–H6 builder scope**. Capture in ARCHITECTURE.md when each is designed.

| Priority | Feature | Why later |
|----------|---------|-----------|
| **P1** | Gemini native JSON + SSE extractors | Users ask for Google models |
| **P1** | OpenAI-compatible upstream picker (Grok / custom base URL) in org settings | Model-agnostic without N parsers |
| **P1** | Self-serve signup page (email → Stripe → key shown once) | Reduce manual onboarding |
| **P2** | Real dashboard (React): charts, bucket CRUD, invite members | Demand for UI beyond `/ops` |
| **P2** | Slack interactive top-up buttons | Nice-to-have after webhook works |
| **P2** | Email alerts (Resend/Postmark) | Users without Slack |
| **P2** | CSV export + longer retention / audit export | Team plan upsell |
| **P3** | Hold provider keys in vault (optional) | Higher trust / complexity |
| **P3** | SSO / multi-seat Team | Enterprise-ish |
| **P3** | Prometheus `/metrics` + status page | Ops maturity |
| **P3** | Community self-hosted free tier (docs only) | Lead gen; paid = hosted |

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

---

## Definition of Done (MVP)

- [x] Proxy forwards OpenAI chat requests transparently (stream + non-stream) — Day 1
- [x] Concurrent requests respect budget via reservation — Day 2 miniredis test
- [x] Redis outage → requests still forwarded (fail-open) with alert fired — Day 2
- [x] Budget exhausted → 429 without upstream call — Day 2
- [ ] Usage settlement adjusts balance within 1s of stream completion (p99) — Day 3 Phase B
- [ ] No goroutine leaks on 1000 aborted streams (load test) — Day 3 Phase C
- [ ] E2E concurrent overspend prevention with real usage settlement — Day 3 Phase C
