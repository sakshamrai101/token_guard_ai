# AI Token Budgeter — Product & Engineering Plan

## 1. Problem Statement

An AI budget enforcement proxy that sits between application and LLM provider APIs, preventing runaway spend by enforcing per-bucket token/cost budgets in real time. It functions as an intelligent circuit breaker: block requests when budget is exhausted, track actual usage from provider responses, and **never take down production LLM traffic** when the enforcement layer itself is degraded.

**Non-goals (MVP):** Multi-provider cost normalization dashboard, billing/invoicing, client SDK, WebSocket/gRPC protocols.

---

## 2. Technical Architecture

### 2.1 Components

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Data plane | Go reverse proxy (`net/http/httputil` + custom `ResponseWriter`/`Reader` tap) | Low latency, first-class concurrency |
| State store | Redis 7+ with Lua scripts | Atomic reservation + settlement |
| Upstream | Transparent pass-through to OpenAI / Anthropic HTTPS APIs | Drop-in replacement |
| Downstream | SSE streaming passthrough (no full-body buffer) | Provider-compatible streaming |
| Alerts | Slack webhook (budget exhausted, fail-open, settlement failures) | Minimal ops visibility |

### 2.2 Budget Lifecycle (Reservation + Settlement)

Two-phase atomic budget management — **not** check-then-async-decrement:

1. **Pre-request (sync, blocking):** Lua script `reserve_budget` atomically holds `estimated_cost` against bucket balance. Returns `{allowed, reservation_id, remaining}` or `{allowed: false}`.
2. **Post-response (async, non-blocking):** On usage extraction, Lua script `settle_budget` reconciles `actual_cost` vs reserved amount, releases hold, adjusts balance. Idempotent on `request_id`.
3. **TTL safety net:** Unsettled reservations expire after `RESERVATION_TTL` (default 5m) via Redis key TTL; auto-release on proxy crash.

```text
Client → Proxy → [reserve_budget (Redis)] → Provider
                      ↓ (if allowed)
              Stream passthrough → Client
                      ↓ (async)
              settle_budget (Redis) ← usage extracted from response

