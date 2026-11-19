---
name: sermon-proto-change
description: Safely change SerMon's Protobuf/Connect-RPC schemas in proto/ (agent↔server and web↔server APIs) with backward compatibility, validation rules and code generation for Rust, Go and TypeScript. Use whenever a message, field, enum or RPC is added, changed or removed.
---

# Change the protocol

Reference: KONZEPT.md §5 (protocol), §21 (versioning: server supports agent N and N-1).

## Rules
- Packages are versioned: `sermon.agent.v1`, `sermon.web.v1`. Breaking changes require a new version package – avoid.
- **Never** reuse or renumber field numbers; removed fields → `reserved` (number and name).
- Adding fields/enum values is fine; new enum values must be handled by older peers (always have `*_UNSPECIFIED = 0` and treat unknown as "ignore/unsupported").
- New server → agent command? The agent must reject unknown commands gracefully and report `CommandResult{unsupported}`. Commands are **typed messages only** – no generic "exec"/string payload fields.
- Identity fields (`org_id`, `node_id`) do **not** belong in agent → server messages; the server takes them from the certificate.
- Add **protovalidate** constraints for every new field (lengths, ranges, enums defined, required). Bound all `repeated`/`bytes`/`string` sizes.
- Document every message/field with comments (they become API docs).

## Steps
1. Edit `proto/sermon/<api>/v1/*.proto`.
2. `just gen` (runs `buf generate` with local plugins only → Rust `agent/crates/sermon-transport/src/gen`, Go `server/internal/gen`, TS `web/src/lib/api/gen`). Never edit generated code and never put own files into these directories (`clean: true` deletes them). Commit schema and generated code together; `just check-gen` must pass.
3. `just lint-proto` (STANDARD + COMMENTS: document everything) and `just breaking-proto` (against `main`) must be green.
   - RPC names must not collide with methods of generated clients (e.g. tonic's `connect()` constructor – that's why the agent RPC is `Session`).
   - New Rust messages: add a roundtrip test in `agent/crates/sermon-transport/tests/proto.rs`.
   - New validation rules: add positive and negative cases to `server/internal/transport/agentapi/schema_test.go` (or the web API equivalent).
4. Write/adjust tests on both sides (TDD): server handler test, agent client test; for N-1 compatibility add a test that decodes a message produced by the previous version (golden binary in `proto/testdata/`).
5. If authz-relevant (new web RPC): add to the role-matrix and tenant-isolation tests (`sermon-security-review` §1).
6. Update KONZEPT.md §5 if the message list changes.

## Checklist
- [ ] No reused field numbers; `reserved` for removals
- [ ] protovalidate constraints, size bounds
- [ ] Generated code refreshed, lint + breaking check green
- [ ] Compatibility test with N-1, authz tests for new RPCs
