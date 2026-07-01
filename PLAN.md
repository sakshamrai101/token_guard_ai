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

- `miniredis` unit tests for Lua scripts
- `reserve_budget` + `settle_budget` wired to pre-request path
- `ENFORCEMENT_MODE=shadow|enforce`
- 429 on exhausted budget
- Fail-open on Redis timeout/unreachable + `fail_open_total` metric
- Slack alert on fail-open and budget denied events

### Day 3 — Usage Extraction & Hardening

- SSE parser with OpenAI usage extractor
- Anthropic usage extractor (basic)
- Non-streaming JSON usage settlement
- Client-disconnect settlement
- Idempotency via `X-Request-Id`
- Reservation TTL + expiry cleanup
- End-to-end test: N concurrent requests cannot overspend bucket

---

## Open Questions (Resolve Before Day 2)

1. Budget unit: USD micro-cents vs raw tokens?
2. Bucket identity trust model for MVP (static config map vs gateway header)?
3. Default `reservation_estimate` per model (static table vs parse from request JSON)?

---

## Definition of Done (MVP)

- [ ] Proxy forwards OpenAI + Anthropic chat requests transparently (stream + non-stream)
- [ ] Concurrent requests respect budget via reservation (demonstrated by test)
- [ ] Redis outage → requests still forwarded (fail-open) with alert fired
- [ ] Budget exhausted → 429 without upstream call
- [ ] Usage settlement adjusts balance within 1s of stream completion (p99)
- [ ] No goroutine leaks on 1000 aborted streams (load test)
