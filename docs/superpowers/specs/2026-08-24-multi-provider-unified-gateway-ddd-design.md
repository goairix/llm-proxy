# Multi-Provider Unified LLM Gateway — DDD Architecture Design

**Date:** 2026-08-24

**Status:** Approved in brainstorming; pending written-spec review

**Repository:** `github.com/goairix/llm-proxy`

## 1. Context

The current project is a transparent reverse proxy with two fixed public route families:

- `/openai/*` forwards to the configured OpenAI-compatible upstream.
- `/anthropic/*` forwards to the configured Anthropic-compatible upstream.

That path deliberately performs no protocol conversion. Its middleware order, SSE behavior, API-key forwarding, rate limiting, token observation, logging, and OpenTelemetry semantics are existing compatibility contracts.

The new product adds a separate unified model gateway. It accepts OpenAI-compatible and Anthropic-compatible client requests, routes logical models across multiple provider deployments, converts request and response protocols, and evolves into a self-hosted multi-tenant platform. The existing transparent proxy remains available and behaviorally unchanged.

## 2. Goals

1. Preserve the existing transparent proxy routes and behavior.
2. Add SDK-compatible root endpoints:
   - `POST /v1/chat/completions`
   - `POST /v1/messages`
3. Support OpenAI, Anthropic, Gemini, and generic OpenAI-compatible upstream connectors in the first gateway milestone.
4. Normalize text, image input, tool calls, structured output, streaming, stop reasons, and usage without silently dropping unsupported semantics.
5. Support logical model aliases and explicit `provider/model` selection.
6. Provide weighted routing, priority fallback, bounded retries, passive health tracking, and circuit breaking.
7. Authenticate gateway clients with virtual keys and manage upstream credentials server-side.
8. Run as a stateless, horizontally scalable data plane in a single region.
9. Evolve into a PostgreSQL-backed multi-tenant control plane with organization, project, RBAC, audit, budget, usage, and cost domains.
10. Organize the entire codebase with DDD and Clean Architecture, following the broad structure of `/Users/dysodeng/project/go/app-service` while enforcing stricter dependency boundaries.

## 3. Non-goals

- Changing the behavior or public paths of the existing transparent proxy.
- Supporting OpenAI Responses API in the first gateway milestone.
- Supporting audio, files, batch jobs, payments, account top-ups, invoices, or subscription plans in the initial program.
- Standardizing every provider-specific capability into the common protocol.
- Dynamically loading Go plugins or WASM connectors in the first milestone.
- Multi-region active-active deployment in the initial architecture.
- Fetching remote image URLs inside the gateway when an upstream requires inline image bytes.
- Logging prompts, model output, tool arguments, API keys, or provider secrets.

## 4. Selected architecture

The selected approach is a **typed canonical semantic core in a modular monolith**, bounded by DDD layers and outbound ports.

```text
Client
  ├─ /openai/*, /anthropic/*
  │    └─ existing transparent proxy pipeline
  │
  └─ /v1/chat/completions, /v1/messages
       └─ protocol ingress adapter
          └─ inference application service
             ├─ virtual-key authentication and access policy
             ├─ capability validation
             ├─ model resolution and route planning
             ├─ quota reservation
             ├─ provider connector attempts
             └─ usage/cost metering
                └─ canonical response or event stream
                   └─ protocol egress adapter
```

The canonical model is not an untyped universal JSON document. It consists of stable, typed common semantics plus explicitly namespaced provider extensions.

## 5. DDD and Clean Architecture

### 5.1 Top-level layers

```text
internal/
├── interfaces/       # HTTP/CLI adapters, protocol DTOs, handlers, router, middleware
├── application/      # use cases, commands/results, orchestration, application ports
├── domain/           # aggregates, value objects, domain services, ports, repositories
├── infrastructure/   # HTTP server, providers, persistence, Redis, secrets, config, OTel
└── di/               # Wire sets, modules, and composition root
```

Dependency direction is fixed:

```text
interfaces → application → domain
infrastructure → domain/application ports
di → all layers for assembly only
```

The domain layer may use the Go standard library and intentionally selected identity/value libraries. It must not import provider SDKs, `net/http`, GORM, Redis, Viper, OpenTelemetry, Zap, or any `infrastructure` package. Infrastructure entities do not double as domain aggregates.

Google Wire is used under `internal/di` in the same style as the reference project. Generated Wire code is checked in and regenerated only when the dependency graph changes.

### 5.2 Bounded contexts

#### Inference

Owns canonical message/content types, tool definitions and calls, structured-output constraints, generation options, stop reasons, normalized usage, provider attempts, and typed stream events.

Key types include:

- `InferenceRequest`
- `Message`
- `ContentBlock`
- `ToolDefinition`
- `ToolChoice`
- `OutputConstraint`
- `InferenceResponse`
- `StreamEvent`
- `Usage`
- `AttemptResult`

The provider connector port belongs to this context because its contract is expressed entirely in inference domain types.

#### Model Catalog

Owns provider kinds, provider deployments, upstream model identities, model aliases, capability sets, route targets, route policies, and immutable route plans.

Key aggregates and values include:

- `ProviderDeployment`
- `ModelDefinition`
- `ModelAlias`
- `CapabilitySet`
- `RoutePolicy`
- `RouteTarget`
- `RoutePlan`

#### Access

Owns organizations, projects, virtual keys, roles, project model policy, and authorization decisions. The initial self-hosted milestone uses a platform scope but preserves organization and project identity in contracts.

#### Credential

Owns credential metadata, platform versus tenant scope, provider binding, secret references, activation state, and rotation version. It never owns or exposes provider SDK objects.

#### Metering

Owns immutable usage facts, attempt-level cost, versioned pricing, quota reservations, project budgets, reconciliation, and cost ledger entries.

#### Shared kernel

Contains only stable cross-context IDs, domain error primitives, clocks, and domain-event primitives. Convenience helpers do not belong in the shared kernel.

### 5.3 Target package layout

```text
internal/
├── domain/
│   ├── inference/{model,valueobject,service,port,errors,event}/
│   ├── modelcatalog/{model,valueobject,service,repository,errors,event}/
│   ├── access/{model,valueobject,service,repository,errors,event}/
│   ├── credential/{model,valueobject,service,repository,port,errors,event}/
│   ├── metering/{model,valueobject,service,repository,port,errors,event}/
│   └── shared/{valueobject,errors,event,port}/
├── application/
│   ├── inference/{service,dto/command,dto/result}/
│   ├── modelcatalog/{service,dto}/
│   ├── access/{service,dto}/
│   ├── credential/{service,dto}/
│   └── metering/{service,dto,event/handler}/
├── interfaces/
│   ├── http/
│   │   ├── handler/{transparent,inference,admin,dashboard}/
│   │   ├── dto/request/{openai,anthropic,admin}/
│   │   ├── dto/response/{openai,anthropic,admin}/
│   │   ├── middleware/
│   │   ├── router/
│   │   └── validator/
│   └── cli/command/
├── infrastructure/
│   ├── provider/{openai,anthropic,gemini,openaicompatible}/
│   ├── proxy/
│   ├── persistence/{entity,repository,migration,transaction}/
│   ├── cache/redis/
│   ├── metering/redisstream/
│   ├── secrets/{environment,file,dbencrypted}/
│   ├── snapshot/{file,postgres}/
│   ├── config/
│   ├── server/http/
│   ├── observability/
│   └── logger/
└── di/{modules,provider}/
```

Directories are created when their phase begins; empty architecture scaffolding is not added in advance.

### 5.4 Existing-package migration

| Current package | Target responsibility |
| --- | --- |
| `internal/proxy` | `internal/infrastructure/proxy` |
| `internal/middleware` | HTTP concerns to `interfaces/http/middleware`; technical backends to infrastructure |
| `internal/config` | `internal/infrastructure/config` |
| `internal/observability` | `internal/infrastructure/observability` |
| `internal/logger` | `internal/infrastructure/logger` |
| `internal/dashboard` | handler/UI under `interfaces/http/handler/dashboard`; read model outside domain |
| `internal/tokenusage` | transparent-proxy observation under infrastructure; new gateway usage under `domain/metering` and connector adapters |
| `internal/server` | router under interfaces and server lifecycle under `infrastructure/server/http` |

Moving existing code is a refactor, not a behavior change. Characterization tests lock middleware order, routes, SSE flushing, token semantics, health checks, OTel propagation, shutdown order, logging, and rate limiting before packages move.

The transparent proxy is a technical adapter, not a bounded context. It does not pass through the inference canonical model.

## 6. Public HTTP contracts

### 6.1 Existing transparent contracts

- `/openai/*` retains upstream key passthrough and OpenAI path rewriting.
- `/anthropic/*` retains upstream key passthrough and Anthropic path rewriting.
- `/healthz`, `/readyz`, and the existing dashboard retain their current semantics.

### 6.2 Unified gateway contracts

- `POST /v1/chat/completions` implements OpenAI Chat Completions-compatible JSON and SSE.
- `POST /v1/messages` implements Anthropic Messages-compatible JSON and SSE.
- `/v1/chat/completions` reads the virtual gateway key from `Authorization: Bearer <key>`.
- `/v1/messages` reads the virtual gateway key from `x-api-key` and accepts Bearer authentication as a documented gateway extension. It validates `anthropic-version` against the ingress adapter's supported versions.
- Neither endpoint interprets the virtual key as an upstream provider credential or forwards it upstream.
- Official SDKs must work by changing only the base URL and gateway key, subject to the supported capability matrix.
- Unknown public fields are rejected unless the ingress protocol explicitly permits them or they occur inside the documented provider-extension namespace.

The OpenAI and Anthropic DTOs remain in the interface layer. Neither DTO is used as a domain model or passed directly to a provider connector.

## 7. Canonical inference model

### 7.1 Request semantics

The common request supports:

- system/developer guidance with preserved ordering semantics;
- user and assistant messages;
- text blocks;
- inline image blocks and provider-passable image URLs;
- tool definitions, tool choice, tool calls, and tool results;
- parallel tool calls where supported;
- JSON and JSON Schema output constraints;
- temperature, top-p, maximum output, stop sequences, and seed when supported;
- stream mode and client metadata;
- a requested logical model identity;
- namespaced provider options.

The model records whether a value was absent or explicitly provided when that distinction affects provider defaults.

### 7.2 Provider extensions

Provider-specific fields live in a namespaced map such as:

```json
{
  "provider_options": {
    "gemini": {},
    "anthropic": {}
  }
}
```

Each connector owns a versioned schema for its namespace. Extensions are accepted only when the selected or candidate route targets match that provider kind and the caller is authorized. Arbitrary header injection, arbitrary URL fields, and cross-provider forwarding are forbidden.

### 7.3 Typed stream events

Provider responses are decoded into a transport-independent event stream:

- `ResponseStart`
- `ContentBlockStart`
- `TextDelta`
- `ToolCallStart`
- `ToolArgumentsDelta`
- `ContentBlockStop`
- `UsageUpdate`
- `ResponseFinish`
- `StreamError`

Events preserve block index, tool-call ID, normalized stop reason, normalized usage, and a connector-private original value where required for audit/debugging. Provider SDK types never cross the connector boundary.

The event stream is pull-based or iterator-based so cancellation and backpressure propagate naturally. Connectors never receive an `http.ResponseWriter`.

## 8. Request lifecycle

1. The HTTP ingress adapter validates the protocol-specific request and produces an application command.
2. The inference application service authenticates the virtual key and obtains tenant/project/policy context.
3. The protocol mapper builds an `InferenceRequest`.
4. The model resolver resolves a logical alias or explicit `provider/model` selector.
5. Capability, provider-extension, access, and budget checks filter invalid targets.
6. The quota service reserves estimated request, token, and cost capacity.
7. The router selects an eligible target and invokes its provider connector.
8. The connector encodes the native request and decodes JSON or SSE into canonical response events.
9. The egress adapter encodes canonical response events into the original client protocol.
10. Attempt-level usage and cost events are reconciled and persisted asynchronously through the durable metering path.

Client cancellation, context deadlines, backpressure, and trace context propagate through every step.

## 9. Provider connector contract

A connector declares:

- provider kind and connector version;
- supported capabilities;
- supported provider-extension schema version;
- request encoder;
- non-stream response decoder;
- stream decoder;
- error classifier;
- usage normalizer;
- authentication/header injector;
- optional health observation hooks.

Initial built-in connectors are OpenAI, Anthropic, Gemini, and generic OpenAI-compatible. They are compiled into the binary and registered through Wire provider sets. Adding a built-in connector requires no change to ingress protocols or the inference use case.

A future external-adapter protocol may implement the same semantic port over a versioned RPC boundary. Runtime Go plugins and WASM are not part of the first architecture.

## 10. Capabilities

Capabilities are explicit values, not inferred from a provider name. A model target may declare support for:

- text input/output;
- image input by URL and/or inline data;
- tools and parallel tools;
- streaming text;
- streaming tool arguments;
- JSON output;
- JSON Schema output;
- usage in normal and streaming responses;
- provider-specific extensions.

The ingress mapper derives required capabilities from each request. Routing removes targets that cannot satisfy every required common capability. The gateway returns a protocol-native capability error if no eligible target remains. It never silently deletes a message block, tool definition, output constraint, or provider option.

If an image URL would need to be downloaded and converted to inline bytes for a target, that target is ineligible in the first milestone. The client must supply inline image data or select a URL-capable target.

## 11. Model resolution and routing

### 11.1 Model selection

- A logical name such as `fast-chat` resolves through an immutable, versioned model catalog snapshot.
- An explicit `provider/model` selector splits at the first `/`, constrains the provider kind and upstream model, and still passes authorization, capability, budget, credential, and circuit checks. The upstream model portion may itself contain `/` characters.
- Multiple provider deployments of the same provider kind may remain eligible after explicit selection.
- The response `model` field preserves the client-requested logical identity. Actual target identity is available only in authorized debugging, logs, audit, and traces.

### 11.2 Route targets

A target contains:

- provider deployment ID;
- upstream model ID;
- credential reference and scope;
- priority and weight;
- per-attempt timeout;
- enabled capabilities;
- retry classification policy;
- price version;
- optional tenant/project restrictions.

### 11.3 Selection order

1. Filter by tenant/project authorization.
2. Filter by request capabilities.
3. Filter by provider-extension compatibility.
4. Check quota and budget eligibility.
5. Remove disabled or open-circuit targets.
6. Select by weight within the best remaining priority group.

Randomness and clocks are injected so route decisions are deterministic in tests.

### 11.4 Retry and failover

- Retries are bounded by a total request deadline and maximum attempt count.
- Connection failures, configured timeouts, 429 responses, and selected 5xx/overload classes may be retried.
- Authentication, authorization, validation, and other non-retriable 4xx responses are not retried.
- Retry honors upstream `Retry-After` only when it fits inside the remaining deadline.
- Passive attempt outcomes drive circuit state. Active checks are optional and must not create billable model generations.
- Once the first downstream response event is written, the upstream target is committed and no retry or failover is allowed.
- Provider-specific options cannot be rewritten merely to make a fallback target eligible.

One logical request may have multiple attempt records. All known provider costs are recorded even when an earlier attempt was not returned to the client.

## 12. Access and tenancy

The target hierarchy is:

```text
Organization
  └─ Project
      ├─ Virtual Keys
      ├─ Allowed Models and Route Policies
      ├─ Quotas and Budgets
      └─ Usage and Cost Views
```

The initial self-hosted data plane uses a platform organization/project bootstrap record. IDs and scope remain present in application and domain contracts so multi-tenancy does not require changing the inference API.

Virtual keys are high-entropy random values with an identifiable non-secret prefix. The control plane returns plaintext only once at creation. Persistent lookup uses an HMAC-SHA-256 fingerprint with a server-side pepper; logs retain only a non-secret key ID or safe suffix.

The existing transparent-proxy limiter continues to key on forwarded upstream credentials and keeps its current semantics. Unified-gateway rate limits, quotas, and budgets key on virtual-key/project context. The two limiter namespaces and stores are not shared.

The future control plane provides organization membership, project roles, service accounts, RBAC, and immutable audit events. Payment and invoice domains remain outside scope.

## 13. Credentials and secrets

The system supports both credential modes:

1. A platform credential pool, implemented first.
2. Tenant-owned BYOK credentials, enabled in a later phase.

Every route target binds to a credential ID and credential scope. Usage, cost, and audit records preserve whether a platform or tenant credential was used.

The `SecretResolver` domain port accepts a secret reference and returns a short-lived secret value. Planned infrastructure adapters are:

- `env://NAME`
- `file:///absolute/path`
- `db-encrypted://credential/<id>`

The database adapter uses AES-256-GCM envelope encryption. Each stored secret includes cipher version, nonce, encrypted data, and key-encryption-key ID. Root/key-encryption keys live outside PostgreSQL and are supplied through an environment/file key provider initially; KMS/Vault implementations may be added later. Rotation writes a new secret version before retiring the old version.

Resolved plaintext may be cached for a short, configurable TTL and is invalidated by credential version changes. Plaintext never appears in runtime snapshots, logs, traces, metrics, errors, audit payloads, or API responses.

Credential deletion is staged: disable dependent route targets, publish and confirm a new snapshot, revoke/delete the upstream secret, and finally remove recoverable metadata according to retention policy.

## 14. Control plane and runtime snapshots

PostgreSQL is the source of truth for control-plane resources. Redis is never the source of truth for configuration.

An administrative write performs the following in one database transaction:

1. Validate RBAC and domain invariants.
2. Write resource changes.
3. Write an immutable audit event.
4. Write an outbox event.

The snapshot compiler reads a consistent configuration version, resolves references, validates route and capability invariants, and emits an immutable `RuntimeSnapshot`. The snapshot contains virtual-key fingerprints, access policy, model catalog, route plans, deployment metadata, price-version references, and credential references. It never contains plaintext secrets.

Data-plane replicas load and validate a new snapshot before atomically replacing the current pointer. Existing requests retain the old snapshot; new requests use the new snapshot. A failed snapshot leaves the last-known-good version active and raises readiness detail, metrics, logs, and alerts.

Redis pub/sub or streams notify replicas of a new version, but replicas also poll the authoritative version to recover from missed notifications.

During the first data-plane milestone, a YAML snapshot source compiles into the exact same `RuntimeSnapshot`. The later PostgreSQL control plane replaces only the source/compiler adapter; inference, routing, and provider connectors remain unchanged. Deployment-level settings such as server ports, logs, and OTel remain file/environment configuration even after dynamic resources move to PostgreSQL.

## 15. Deployment topology

Target production topology is single-region, multi-replica:

```text
Load Balancer
  └─ stateless data-plane replicas
       ├─ in-memory RuntimeSnapshot
       ├─ short-lived secret cache
       ├─ PostgreSQL control-plane/ledger access through adapters
       └─ Redis distributed quota, circuit coordination, notification, usage stream
```

Route configuration is never queried from PostgreSQL on each request. Redis operations remain on the hot path only where cross-replica semantics are required: distributed rate limiting, quota reservation/reconciliation, strict budgets, and shared circuit coordination.

Local development can use in-memory adapters and a YAML snapshot. These adapters are explicitly non-distributed and non-strict; production startup rejects an invalid combination when multi-replica or strict budget mode is enabled without the required backend.

## 16. Metering, cost, and budget

### 16.1 Reservation

Before the upstream call, the gateway estimates input tokens and uses the requested maximum output to reserve request count, tokens, and estimated cost atomically in Redis. The reservation is scoped to tenant, project, virtual key, and configured policy windows. Strict budget mode fails closed when Redis is unavailable.

Reservation also creates a pending metering fact before provider work begins.

### 16.2 Attempt-level usage

Each provider attempt has a deterministic event ID derived from logical request ID and attempt number. A usage event records:

- tenant, project, and virtual-key IDs;
- logical request and attempt IDs;
- requested model identity;
- provider deployment and upstream model IDs;
- credential ID and scope;
- raw provider usage;
- normalized usage;
- usage presence state;
- price version;
- attempt outcome and timestamps.

The final client response exposes only the successful returned attempt's protocol-compatible usage. The internal ledger records every known billable attempt. Missing usage is a first-class state and is never treated as zero.

### 16.3 Durable delivery and ledger

Production uses Redis Streams as the first durable usage sink. A metering worker consumes at least once, calculates cost with the pinned immutable price version, writes the PostgreSQL ledger idempotently, and reconciles the reservation. A unique event-ID constraint prevents duplicate charges.

PostgreSQL ledger rows are immutable. Corrections are compensating entries, not updates. Aggregated reporting tables are rebuildable projections, not billing facts.

The Redis deployment used for strict metering requires persistence and replication appropriate to the deployment's loss tolerance. Failed event publication or an uncompleted pending fact produces a critical alert and reconciliation work item. The self-hosted in-memory sink is marked best-effort and cannot claim strict budget enforcement.

## 17. Error model

Canonical error categories include:

- invalid request;
- authentication failure;
- authorization failure;
- unknown or unavailable model;
- unsupported capability;
- rate limited;
- budget exceeded;
- upstream timeout;
- upstream overloaded/unavailable;
- internal failure.

Before response headers are committed, the egress adapter maps the canonical error to the selected ingress protocol's native status and error envelope. Every response has a stable gateway request ID. Provider request IDs are captured internally and exposed only through authorized diagnostics.

After SSE begins, HTTP status cannot change. The egress adapter emits a protocol-compatible in-stream error where supported and terminates the stream in a way covered by official-SDK compatibility tests. It never switches providers after stream commitment.

Error messages disclose no upstream credentials, secret references, internal network addresses, raw provider bodies, tenant existence, or unredacted request content.

## 18. Security

- Provider base URLs are administrative resources, never request parameters.
- Base URLs allow only configured HTTP(S) schemes and pass hostname, DNS/IP, redirect, and private-network policy checks.
- `provider_options` are versioned, schema-validated, and provider-scoped.
- Arbitrary request-header forwarding is forbidden on unified gateway routes.
- Request body size, inline image size, JSON depth, message count, tool count, schema size, concurrency, idle timeout, and total deadline are bounded.
- The gateway does not fetch remote image URLs for protocol conversion in the first milestone.
- Prompt and completion content are excluded from logs, traces, metrics, and audit by default.
- Metrics never use API key, tenant ID, project ID, model ID, raw path, query, or resource ID as attributes.
- Secret values are redacted at construction boundaries and never formatted into generic errors.
- Tenant isolation tests cover every repository, cache key, snapshot lookup, administrative command, and usage query.
- Trusted reverse-proxy headers are honored only when the immediate peer is configured as trusted.

## 19. Observability

The server trace contains child spans for authentication/policy, model resolution, routing, quota reservation, and each provider attempt. Attempt spans preserve parent context and inject `traceparent` upstream where supported.

Low-cardinality metrics include ingress protocol, normalized endpoint, provider connector kind, outcome, retry class, circuit state, quota rejection reason, missing usage, snapshot version health, secret-resolution failures, and metering lag. Dynamic model, tenant, key, prompt, and raw route values are excluded from metric attributes.

Structured logs may include internal request ID, attempt ID, safe tenant/project resource IDs, route-policy version, deployment ID, normalized endpoint, status, bytes, and latency. They may not include request/response bodies or secret values.

The existing transparent-proxy OTel URL normalization, parent-child span semantics, and low-cardinality rules remain unchanged during migration.

## 20. Testing strategy

### 20.1 Characterization and architecture tests

- Lock existing transparent-proxy routes, middleware order, rate-limit semantics, SSE flush behavior, token observation, health/readiness, OTel propagation, logging, and shutdown behavior before moving packages.
- Add import-boundary tests or lint rules that reject domain imports from application, interfaces, or infrastructure and reject application imports from interfaces/infrastructure.

### 20.2 Protocol conformance

- Maintain golden JSON/SSE fixtures for both ingress protocols.
- Exercise text, image, tools, parallel tools, structured output, stop reasons, usage, and errors.
- Run black-box compatibility tests through official OpenAI and Anthropic SDKs using the local server.
- Handle arbitrary network chunks, CRLF, multi-line SSE data, unknown events, partial tool JSON, streams without trailing separators, early disconnects, and final usage omissions.

### 20.3 Connector contract suite

Every connector runs the same contract suite against `httptest.Server` fakes. Default tests do not call real providers. Optional credential-gated smoke tests run separately and never print secrets or content.

### 20.4 Routing and resilience

- Inject clock, random source, and transport to test deterministic weighted selection.
- Test retry classification, total deadline, `Retry-After`, priority fallback, circuit transitions, and the no-failover-after-first-event invariant.
- Use fuzz/property tests for canonical mapping and stream decoders.
- Verify cancellation, backpressure, goroutine cleanup, and first-event flush before upstream completion.

### 20.5 Persistence and tenancy

- Run PostgreSQL and Redis integration suites separately from fast unit tests.
- Verify idempotent usage consumption, immutable ledger corrections, reservation reconciliation, outbox publication, snapshot atomicity, and last-known-good behavior.
- Run tenant-isolation, SSRF, provider-option validation, virtual-key hashing, secret rotation, and redaction tests.

### 20.6 Release gates

- Affected package tests, then `go test ./...`.
- `go test -race ./...` for concurrent routing, snapshots, metering, cache, and statistics changes.
- `go build -o /tmp/llm-proxy ./cmd/proxy`.
- `git diff --check`.
- Protocol compatibility suite and streaming contract suite.
- Performance comparison against a checked-in benchmark baseline; streaming implementations may not buffer the full response.
- Existing transparent-proxy behavior tests must remain green without semantic expectation changes.

## 21. Delivery program

This system is intentionally split into separately specified and planned subprojects.

### Phase 1 — DDD foundation and unified inference core

- Capture transparent-proxy characterization tests before moving packages.
- Add DDD/Clean Architecture layers and Wire composition.
- Move existing technical packages in small dependency-safe steps without behavior changes, keeping the build and full regression suite green after each move.
- Add `/v1/chat/completions` and `/v1/messages`.
- Implement canonical request/response/event types.
- Implement virtual-key authentication with a bootstrap platform scope.
- Implement YAML-to-`RuntimeSnapshot` compilation.
- Implement capability validation and provider options.
- Implement OpenAI, Anthropic, Gemini, and generic OpenAI-compatible connectors.
- Implement weighted routing, priority fallback, bounded retries, and local circuit state.
- Implement platform credentials through `env://` and `file://`.

### Phase 2 — Production data plane and durable metering

- Add PostgreSQL and Redis infrastructure adapters.
- Add distributed rate limits, quota reservations, shared circuit coordination, and configuration notifications.
- Add Redis Stream usage transport, metering worker, price versions, PostgreSQL ledger, reconciliation, and budgets.
- Add `db-encrypted://` Secret Provider with external root-key handling and rotation.
- Add CLI bootstrap commands that reuse credential application services to put, rotate, disable, and inspect encrypted credential metadata before the full administrative API exists.
- Validate multi-replica behavior and failure modes.

### Phase 3 — Multi-tenant control plane

- Add organization, project, membership, service account, virtual-key, RBAC, and audit aggregates/use cases.
- Add administrative APIs, repositories, migrations, transaction boundaries, and outbox processing.
- Add PostgreSQL snapshot compiler and versioned publication.
- Add platform credential-pool management and usage/cost query projections.

### Phase 4 — Ecosystem expansion

- Enable tenant BYOK.
- Add OpenAI Responses API ingress.
- Define and implement an external provider-adapter RPC contract.
- Add more native providers and Secret backends.
- Add dynamic cost/latency/region policy and, only when required, multi-region topology.

Each phase receives its own design refinement and implementation plan before code changes. The next plan after this umbrella design is approved is **Phase 1: DDD foundation and unified inference core**.

## 22. Acceptance criteria for the target design

The architecture is considered realized when:

1. Existing `/openai/*` and `/anthropic/*` clients observe no behavior regression.
2. Official OpenAI and Anthropic SDK compatibility suites pass against the new root endpoints for the supported capability matrix.
3. The four initial connector kinds pass a common connector contract suite.
4. Unsupported semantic combinations produce explicit protocol-native errors.
5. Streaming forwards the first upstream event before upstream completion and propagates cancellation/backpressure without goroutine leaks.
6. Route selection, retries, and circuit breaking obey the documented deterministic rules and never fail over after response commitment.
7. Multi-replica deployments enforce distributed quotas and record attempt-level usage idempotently.
8. PostgreSQL control-plane changes publish atomic snapshots, and invalid snapshots cannot replace last-known-good state.
9. Platform and tenant credentials remain scoped, encrypted/referenced, rotatable, redacted, and auditable.
10. Domain packages have no infrastructure or transport dependencies.

## 23. Protocol references

- [OpenAI Chat API reference](https://developers.openai.com/api/reference/resources/chat)
- [Anthropic Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [Anthropic API errors](https://platform.claude.com/docs/en/api/errors)
- [Gemini function calling and streamed arguments](https://ai.google.dev/gemini-api/docs/function-calling)
- [Gemini structured output](https://ai.google.dev/gemini-api/docs/structured-output)

These references are implementation inputs, not stable internal contracts. Provider wire formats remain isolated inside versioned interface and infrastructure adapters.
