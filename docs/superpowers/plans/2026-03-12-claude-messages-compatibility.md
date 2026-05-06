# Claude Messages Compatibility Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/v1/messages` easier to trust for Claude Code by documenting a Claude Code-focused compatibility subset, expanding the compatibility test matrix, tightening SSE and error semantics where tests expose gaps, and updating outward-facing docs to match reality.

**Architecture:** Keep the implementation centered in the existing `openaihttp` Claude compatibility layer. First lock down the current support boundary in docs and tests, then use the failing tests to drive small changes in `openaihttp/claude.go` and closely related helpers until the documented behavior is real and regression-tested.

**Tech Stack:** Go, `net/http`, Gin route registration, testify, Claude CLI integration tests, Markdown docs

---

## File Structure

### Existing files to modify

- `openaihttp/claude.go`
  - Main Anthropic Messages compatibility handler.
  - This is where request validation, tool choice handling, SSE event generation, stop reason selection, and usage shaping currently live.
- `openaihttp/claude_test.go`
  - Main unit/handler test file for `/v1/messages`.
  - Already contains useful SSE parsing helpers and tool protocol tests; extend it instead of creating parallel test files.
- `openaihttp/integration_claude_teammate_cli_test.go`
  - Real Claude CLI round-trip integration coverage.
  - Expand this carefully for high-value scenarios only.
- `README.md`
  - Top-level product positioning; update wording so it does not over-claim full Anthropic compatibility.
- `docs/API.md`
  - Public API contract summary; add the support matrix and boundary notes here.
- `docs/TESTING.md`
  - Add guidance for the Claude compatibility matrix and how to run the important tests.

### New files to create

- `docs/superpowers/plans/2026-03-12-claude-messages-compatibility.md`
  - This plan document.
- `docs/CLAUDE_CODE_COMPATIBILITY.md`
  - A focused support matrix for Claude Code practical compatibility.
  - Keep this concise and linkable from `README.md` and `docs/API.md`.

### Files to inspect while implementing

- `openaihttp/compat_toolcall_test.go`
  - Already covers tool argument normalization/dedup semantics; reuse patterns rather than rewriting logic blindly.
- `openaihttp/compat.go`
  - Existing shared compatibility patterns for request validation and streaming.
- `ARCHITECTURE.md`
  - Useful if wording updates need alignment with current module boundaries.
- `docs/superpowers/specs/2026-03-12-claude-messages-compatibility-design.md`
  - Source of truth for scope and non-goals.

---

## Chunk 1: Document the Claude Code compatibility boundary

### Task 1: Add a focused compatibility matrix document

**Files:**
- Create: `docs/CLAUDE_CODE_COMPATIBILITY.md`
- Reference: `docs/superpowers/specs/2026-03-12-claude-messages-compatibility-design.md`
- Reference: `docs/API.md`
- Reference: `README.md`

- [ ] **Step 1: Write the failing docs test mentally by defining the exact sections this file must contain**

The new document must include these sections:

```md
# Claude Code Compatibility

## Scope
## Supported request fields
## Supported response behaviors
## Supported streaming behaviors
## Supported teammate tools
## Known gaps / partial support
## Verification sources
```

- [ ] **Step 2: Verify the repository has no existing equivalent matrix doc**

Run: `grep -R "Claude Code Compatibility" docs README.md`
Expected: no existing dedicated support-matrix document

- [ ] **Step 3: Write the new document with the approved support model**

Include a compact matrix like:

```md
| Area | Status | Notes |
| --- | --- | --- |
| `model` / `messages` / `max_tokens` / `stream` | Supported | Covered by handler tests |
| `tools` / `tool_choice` common modes | Partially supported | Supported for Claude Code common paths |
| `tool_use` / `tool_result` | Supported | Includes teammate flows |
| SSE text streaming | Supported | Claude-style SSE emitted |
| SSE `input_json_delta` for tool input | Supported with tests | Important for Task/Agent flows |
| `usage` exact Anthropic parity | Partial | Current values are compatibility-oriented |
```

Do not claim full Anthropic compatibility.

- [ ] **Step 4: Review the file for over-claims**

Check manually that phrases like these do **not** appear unless backed by proof:

```text
fully compatible
complete Anthropic parity
all Messages features supported
```

Expected: wording consistently says Claude Code-focused subset / practical compatibility.

- [ ] **Step 5: Commit**

```bash
git add docs/CLAUDE_CODE_COMPATIBILITY.md
git commit -m "docs: add Claude Code compatibility matrix"
```

### Task 2: Update README positioning to match the new boundary

**Files:**
- Modify: `README.md`
- Reference: `docs/CLAUDE_CODE_COMPATIBILITY.md`

- [ ] **Step 1: Write the failing docs expectation**

The README introduction and Claude sections should stop implying broad Anthropic compatibility and instead say the project supports a Claude Code-focused compatibility subset.

Expected wording shape:

```md
- 提供 Claude 兼容端点：`/v1/messages`、`/v1/messages/count_tokens`
- 面向 Claude Code 常见使用路径提供 Anthropic Messages 兼容子集
```

- [ ] **Step 2: Edit the introduction bullets and Claude CLI notes**

Make these changes:
- keep endpoint list
- add a link to `docs/CLAUDE_CODE_COMPATIBILITY.md`
- explicitly mention that the matrix is the authoritative compatibility boundary

- [ ] **Step 3: Read the changed sections and verify consistency**

Run: `git diff -- README.md docs/CLAUDE_CODE_COMPATIBILITY.md`
Expected: README wording points to the matrix instead of making unbounded compatibility claims.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/CLAUDE_CODE_COMPATIBILITY.md
git commit -m "docs: clarify Claude compatibility scope"
```

### Task 3: Update API docs to describe supported subset and known partial areas

**Files:**
- Modify: `docs/API.md`
- Reference: `docs/CLAUDE_CODE_COMPATIBILITY.md`

- [ ] **Step 1: Write the failing docs expectation**

`docs/API.md` should no longer describe `/v1/messages` only in generic feature bullets; it should also explain that support is optimized for Claude Code practical usage and point to the matrix.

- [ ] **Step 2: Add a short compatibility-boundary section under `/v1/messages`**

Add text with this structure:

```md
> `/v1/messages` targets Claude Code common usage patterns rather than full Anthropic Messages parity.
> See `docs/CLAUDE_CODE_COMPATIBILITY.md` for the supported subset and known gaps.
```

Also add a short list of partial areas:
- exact usage parity
- edge-case content block combinations
- less-common SSE edge semantics

- [ ] **Step 3: Verify the docs stay concise**

Read the final section and confirm it is a boundary description, not a second giant spec.

- [ ] **Step 4: Commit**

```bash
git add docs/API.md
git commit -m "docs: describe Claude messages support boundary"
```

---

## Chunk 2: Expand the `/v1/messages` test matrix before changing behavior

### Task 4: Add table-driven coverage for `tool_choice` modes

**Files:**
- Modify: `openaihttp/claude_test.go`
- Reference: `openaihttp/claude.go`

- [ ] **Step 1: Write the failing tests for `tool_choice` modes**

Add a table-driven test similar to:

```go
func TestClaudeMessages_ToolChoiceModes(t *testing.T) {
    tests := []struct {
        name           string
        body           string
        wantStatus     int
        wantErrSubstr  string
        wantToolCount  int
    }{
        {
            name: "none disables tools",
            body: `{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Read","input_schema":{"type":"object"}}],"tool_choice":{"type":"none"}}`,
            wantStatus: http.StatusOK,
            wantToolCount: 0,
        },
        {
            name: "any requires tools",
            body: `{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`,
            wantStatus: http.StatusBadRequest,
            wantErrSubstr: "tools is required when tool_choice.type=any",
        },
    }
}
```

Use the `NewChatModel` hook to capture the tools actually passed downstream.

- [ ] **Step 2: Run only the new test**

Run: `go test ./openaihttp -run ToolChoiceModes -v`
Expected: FAIL until the assertions and/or code are aligned.

- [ ] **Step 3: Make the minimal code or assertion changes needed**

Prefer fixing handler behavior only if the new tests expose a real mismatch with the approved design. If current code already behaves correctly, fix only the test scaffolding.

- [ ] **Step 4: Re-run the focused test**

Run: `go test ./openaihttp -run ToolChoiceModes -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add openaihttp/claude_test.go
git commit -m "test: cover Claude tool choice modes"
```

### Task 5: Add explicit tests for non-stream and stream error semantics

**Files:**
- Modify: `openaihttp/claude_test.go`
- Reference: `openaihttp/claude.go`

- [ ] **Step 1: Write failing tests for backend errors**

Add tests for:

```go
func TestClaudeMessages_NonStream_BackendErrorUsesCompatError(t *testing.T) {}
func TestClaudeMessages_Stream_BackendCreationErrorUsesCompatError(t *testing.T) {}
```

Use `NewChatModel` returning `&httpError{Status: http.StatusBadGateway, Message: "backend down"}` for one case and a generic error for another.

Assert:
- status code shape is correct
- message contains the expected compatibility-layer error text
- no success payload leaks through

- [ ] **Step 2: Run the focused tests**

Run: `go test ./openaihttp -run 'ClaudeMessages_(NonStream_BackendError|Stream_BackendCreationError)' -v`
Expected: FAIL if current behavior diverges.

- [ ] **Step 3: Make minimal implementation fixes in `openaihttp/claude.go` if needed**

Only touch the branches that currently bypass `httpStatusFromError` / `httpMessageFromError` or otherwise produce inconsistent error semantics.

- [ ] **Step 4: Re-run the focused tests**

Run: `go test ./openaihttp -run 'ClaudeMessages_(NonStream_BackendError|Stream_BackendCreationError)' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add openaihttp/claude.go openaihttp/claude_test.go
git commit -m "fix: align Claude message error semantics"
```

### Task 6: Add focused SSE sequence assertions for text and tool scenarios

**Files:**
- Modify: `openaihttp/claude_test.go`
- Reference: `openaihttp/claude.go`

- [ ] **Step 1: Write failing tests for event ordering**

Add tests with assertions over parsed event names, for example:

```go
func TestClaudeMessages_Stream_TextEventSequence(t *testing.T) {}
func TestClaudeMessages_Stream_ToolUseEventSequence(t *testing.T) {}
```

Assert exact relative ordering for the important events:
- `message_start`
- `content_block_start`
- `content_block_delta`
- `content_block_stop`
- `message_delta`
- `message_stop`

For tool scenarios, also assert that `message_delta.stop_reason == "tool_use"` appears before `message_stop`.

- [ ] **Step 2: Run the focused SSE tests**

Run: `go test ./openaihttp -run 'ClaudeMessages_Stream_(TextEventSequence|ToolUseEventSequence)' -v`
Expected: FAIL if ordering is currently under-specified or different.

- [ ] **Step 3: Make minimal stream-emitter fixes in `openaihttp/claude.go` if needed**

Do not refactor broadly. Only adjust event emission order or stop reason placement if the tests prove a real compatibility mismatch.

- [ ] **Step 4: Re-run the focused SSE tests**

Run: `go test ./openaihttp -run 'ClaudeMessages_Stream_(TextEventSequence|ToolUseEventSequence)' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add openaihttp/claude.go openaihttp/claude_test.go
git commit -m "test: lock Claude SSE event ordering"
```

---

## Chunk 3: Strengthen Claude CLI integration coverage without overbuilding

### Task 7: Add one more high-value Claude CLI integration path

**Files:**
- Modify: `openaihttp/integration_claude_teammate_cli_test.go`
- Reference: `docs/CLAUDE_CODE_COMPATIBILITY.md`

- [ ] **Step 1: Choose exactly one additional real CLI scenario**

Pick one of these and only one for this iteration:
- plain text non-tool round-trip
- `Agent` bootstrap path if current coverage is Task-dominant
- `tool_choice`-sensitive path if reproducible from Claude CLI

Recommendation: add an `Agent`-first bootstrap scenario if today’s test is mainly `Task`-oriented.

- [ ] **Step 2: Write the failing integration test**

Mirror the existing style:
- fake backend
- local gptb2o server
- real `claude` CLI invocation
- assertions over first-turn schema exposure and second-turn tool result handling

Keep the new test self-contained and skip by default behind `GPTB2O_RUN_CLAUDE_IT=1`.

- [ ] **Step 3: Run only the new integration test when the environment is available**

Run: `GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run '<new test name>' -v`
Expected: either PASS locally with Claude installed or SKIP when unavailable.

If local environment is unavailable, still run the package test without the env var and verify it cleanly skips.

- [ ] **Step 4: Make minimal compatibility adjustments if the real CLI exposes a gap**

Touch `openaihttp/claude.go` only if the new integration test reveals an actual runtime mismatch.

- [ ] **Step 5: Commit**

```bash
git add openaihttp/integration_claude_teammate_cli_test.go openaihttp/claude.go
git commit -m "test: add Claude CLI compatibility coverage"
```

### Task 8: Document how to run the compatibility verification set

**Files:**
- Modify: `docs/TESTING.md`
- Reference: `docs/CLAUDE_CODE_COMPATIBILITY.md`

- [ ] **Step 1: Write the failing docs expectation**

`docs/TESTING.md` should include a dedicated subsection for Claude compatibility verification with both fast local tests and optional real CLI tests.

- [ ] **Step 2: Add the commands exactly**

Include commands like:

```bash
go test ./openaihttp -run ClaudeMessages -v
go test ./openaihttp -run 'ToolChoiceModes|TextEventSequence|ToolUseEventSequence' -v
GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
```

Also explain which ones are expected to skip without a local `claude` binary.

- [ ] **Step 3: Verify the docs match the actual test names in the code**

Run: `go test ./openaihttp -list ClaudeMessages`
Expected: test names referenced in docs exist.

- [ ] **Step 4: Commit**

```bash
git add docs/TESTING.md
git commit -m "docs: add Claude compatibility test guidance"
```

---

## Chunk 4: Use tests to drive narrow behavior fixes and final verification

### Task 9: Tighten any remaining `/v1/messages` behavior gaps exposed by the test matrix

**Files:**
- Modify: `openaihttp/claude.go`
- Modify: `openaihttp/claude_test.go`
- Reference: `openaihttp/compat_toolcall_test.go`

- [ ] **Step 1: Run the focused compatibility suite**

Run: `go test ./openaihttp -run 'ClaudeMessages|ToolChoiceModes|TeammateCLI' -v`
Expected: identify any remaining failures introduced by the new assertions.

- [ ] **Step 2: Fix only the specific failing behavior**

Allowed fix areas:
- stop reason selection
- event ordering
- error mapping consistency
- tool input delta handling
- tool choice filtering behavior

Do not introduce broad abstractions or speculative support for unrelated Anthropic features.

- [ ] **Step 3: Re-run the same focused suite**

Run: `go test ./openaihttp -run 'ClaudeMessages|ToolChoiceModes|TeammateCLI' -v`
Expected: PASS, with the CLI-dependent cases either PASSing or SKIPping cleanly.

- [ ] **Step 4: Commit**

```bash
git add openaihttp/claude.go openaihttp/claude_test.go openaihttp/integration_claude_teammate_cli_test.go
git commit -m "fix: tighten Claude messages compatibility behavior"
```

### Task 10: Run final targeted verification and capture the finished boundary

**Files:**
- Modify: `docs/CLAUDE_CODE_COMPATIBILITY.md` (only if test results changed claimed status)
- Modify: `README.md` (only if test results changed claimed status)
- Modify: `docs/API.md` (only if test results changed claimed status)

- [ ] **Step 1: Run final verification commands**

Run:

```bash
go test ./openaihttp -run ClaudeMessages -v
go test ./openaihttp -run 'ToolChoiceModes|TextEventSequence|ToolUseEventSequence' -v
go test ./openaihttp -run TeammateCLI -v
```

Expected:
- unit/handler tests PASS
- CLI integration test SKIPs cleanly without local Claude, or PASSes when available

- [ ] **Step 2: Adjust the support matrix if verification disproves any claim**

If a feature stayed partial after implementation, keep it marked partial. Do not “upgrade” status labels without test evidence.

- [ ] **Step 3: Review the final diff for scope creep**

Run: `git diff --stat HEAD~1..HEAD`
Expected: changes are limited to Claude compatibility docs/tests/handler logic; no unrelated refactors.

- [ ] **Step 4: Commit**

```bash
git add docs/CLAUDE_CODE_COMPATIBILITY.md README.md docs/API.md docs/TESTING.md openaihttp/claude.go openaihttp/claude_test.go openaihttp/integration_claude_teammate_cli_test.go
git commit -m "feat: harden Claude Code messages compatibility"
```

---

## Plan review checklist

Use this checklist while executing:

- [ ] Every new compatibility claim is backed by a test or explicitly marked partial
- [ ] No doc says or implies full Anthropic parity
- [ ] `openaihttp/claude.go` changes stay narrow and test-driven
- [ ] SSE tests assert ordering, not just substring presence
- [ ] Real Claude CLI coverage remains optional and skip-safe
- [ ] README/API/testing docs all point to the same compatibility matrix

## References

- Spec: `docs/superpowers/specs/2026-03-12-claude-messages-compatibility-design.md`
- Core handler: `openaihttp/claude.go`
- Core tests: `openaihttp/claude_test.go`
- Real CLI test: `openaihttp/integration_claude_teammate_cli_test.go`
- Public docs: `README.md`, `docs/API.md`, `docs/TESTING.md`

Plan complete and saved to `docs/superpowers/plans/2026-03-12-claude-messages-compatibility.md`. Ready to execute?
