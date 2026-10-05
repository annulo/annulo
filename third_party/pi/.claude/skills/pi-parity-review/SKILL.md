---
name: pi-parity-review
description: Adversarially verify that a ported change is faithful to the original pi implementation (TS source + published npm build). Use after porting upstream pi changes, or standalone on any area of this repo ("is X faithful to pi?").
---

# pi-parity-review — is the port true to pi?

Input: the upstream change (sha in the upstream clone) and our ported diff.
You are independent of the porter: assume the port is wrong until the diff
proves otherwise. Self-authored tests are circular — they pin what the porter
believed, not what pi does. Every sweep of this project found real bugs ONLY
via comparison against real pi.

## Working-tree safety (hard rule)
You are reviewing the UNCOMMITTED working tree — it is the only copy of the
port. NEVER mutate it: no `git checkout`/`restore`/`reset`/`stash`/`clean`, no
edits to ported files. To mutation-test (disable a fix and confirm its test
fails), copy the file to a scratch path, mutate the COPY in a throwaway dir
(or `git worktree add` a detached checkout), run tests there, and discard it —
never touch the live file. If you ever do dirty the tree, restore it exactly
and say so loudly in your report.

## References
- Upstream TS: `$PI_UPSTREAM_DIR` (default `~/.cache/pi-upstream`). **Never read
  its working tree — always name a sha**: `git -C dir show <sha>:<path>`,
  `git -C dir diff <sha>^1..<sha>`, `git -C dir grep <pattern> <sha> -- <path>`.
  The checkout is whatever was last checked out and is nobody's contract; during
  the 2026-08-11 cycle it sat at a 2026-06-07 commit, ~2 months behind, so a
  plain `grep` of it reported the OPPOSITE of what the pin contained and read as
  though upstream had reverted the change under review. `/pi-sync` now
  fast-forwards it during preflight, which makes that failure less likely but
  not impossible — and even a correct tree is at `origin/main`, which mid-cycle
  is AHEAD of the pin and so is still not what you are reviewing.
- Published npm build: install/refresh `@earendil-works/pi-ai` +
  `pi-coding-agent` at the matching release into a scratch dir. **When the TS
  source and the shipped build disagree, the BUILD wins** — it's what real pi
  runs and what all goldens come from.
- Goldens in-repo: `coding/testdata/sessparity/`, `coding/testdata/imgparity/`,
  the default-system-prompt golden, and the tool output-string tests.

## Method
1. Read pi's change (the full first-parent diff) until you can state its exact
   observable behavior: strings, constants, ordering, error surfaces, request
   fields, edge cases.
2. Read our ported diff and try to BREAK the correspondence: byte-compare
   every model-visible string (prompts, tool outputs, wrapper text, request
   bodies); check JS semantics ported correctly (UTF-16 `.length`, `??` vs
   `||`, `Math.round`, Number() coercion, insertion order, truthiness of `{}`);
   check the change's edge cases (empty, zero, absent-vs-null, astral chars).
3. **If the change touches request building** (`ai/providers/openai*.go`
   especially): re-run the differential request diff — `difftest/run.sh` (in this repo)
   (scenarios, both capture sides, canon scripts and the Go harness all live in
   `difftest/`; its go.mod `replace` → this repo, and it captures via
   OnPayload returning an error to halt pre-network). It prints per-scenario
   PASS / KNOWN (accepted debt in its `known-divergences.json`) / FAIL / FIXED
   (stale baseline entry), and exits 0 only when every scenario is PASS or
   KNOWN — 1 on any FAIL, 3 on a stale entry. If the directory is missing,
   the runner rebuilds it from the npm build (and, for changes not yet
   released, from upstream TS at the synced sha); see its README to add a
   scenario or re-point it at a new version.
4. If the change touches session format / image decisions / system prompt:
   regenerate or extend the corresponding golden FROM THE NPM BUILD (node
   against the installed package), never by hand.
5. Check whether our existing tests pinned the OLD behavior — a faithful port
   often must update tests; flag any test that now asserts non-pi behavior.

## Generated data / catalog & release changes
- npm reference builds live at `~/.cache/pi-npm/<version>/` (npm i the exact
  version there). Before trusting one, verify authenticity: package-lock
  integrity == `npm view <pkg>@<ver> dist.integrity`.
- **Never verify against the porter's intermediates** — regenerate the
  comparison artifact YOURSELF from the build (the circularity rule applies to
  scratch files, not just tests).
- Canonical catalog: `ai/models_catalog.json` = `JSON.stringify(MODELS)` from
  the matching build's `dist/models.generated.js` (single line, insertion
  order). Review = re-derive and `cmp`.
- **Endpoint pinning** (strongest technique for generated files): show old
  file ≡ upstream data at `<sha>^` AND new file ≡ data at `<sha>` ⇒ the ported
  diff equals the upstream diff exactly.
- Schema drift: enumerate the new artifact's JSON keys/value types against the
  Go struct tags (ai/types.go Model) — unknown keys are silently dropped and a
  type mismatch silently aborts the whole load.
- Release commits carry no portable behavior beyond regenerated artifacts;
  the real changes live in the commits between releases.
- Post-check: `go test ./ai/...` + grep code/tests for removed model ids
  (orphaned defaults, e.g. coding/resolve.go).
- Tip: `node --experimental-strip-types` can execute upstream generated `.ts`
  directly when you need data at a specific source sha.

## Output
Verdict per change: `faithful` / `diverges` (with file:line both sides, the
exact byte/behavior difference, and the failing scenario) / `unverifiable`
(say what's missing — e.g. needs a live key). List any new golden added.
Review-only unless asked to fix.
