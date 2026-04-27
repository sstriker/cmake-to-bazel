# M4: tiered failures + regression detection + fingerprint registry

## Context

M3a produces `<out>/manifest/{converted,failures,determinism}.json` per
orchestrator run, plus the `<out>/cache/actions/<key>/` action-key cache.
M4 turns that data into operator-facing answers: "what changed between
run A and run B?" — both at the per-element fingerprint level and at
the failure-code distribution level.

The plan's stated acceptance gate is "deliberate breakage produces
structured regression report". M4 ships the diff tool, the fingerprint
registry, and the failure-code analytics that satisfy that gate, and
positions M5 (Bazel envelope) to reason about distro stability with
queries instead of file diffs.

Two-phase split kept simple: M4a is the diff layer (read both runs,
emit a typed report); M4b is the persistent fingerprint registry that
spans many runs (history + drift detection across N revisions). M4 in
this plan = both.

## Key decisions

- **No new orchestrator concept.** Regression analysis reads existing
  `<out>/manifest/` files; the orchestrator stays unchanged. The
  registry is a separate file at `<root>/registry/fingerprints.json`
  that the diff tool writes when invoked with `--register`.
- **Schema versioning.** Every output JSON file gains an explicit
  `version: 1` field so M5+ readers can fence on incompatible reads.
  Same rule as `failure-schema.md` and `codegen-tags.md`: append-only
  fields after publication.
- **Reports are JSON-first.** Pretty text rendering is a thin viewer
  on top of the JSON. CI / GH Actions / dashboards can ingest the JSON
  directly.
- **Diff is symmetric, classification is opinionated.** The diff
  identifies four categories (newly-failed, newly-passed,
  fingerprint-drifted, churn) and ranks them by operator priority
  (newly-failed first, fingerprint drift second, etc.). The
  classification rules live in code, not in a config file.

## Step plan with timing

| # | Step | Days | Days (risk-adj) |
|---|---|---:|---:|
| 1 | docs/m4-plan.md + failure-schema cross-reference | 0.5 | 0.5 |
| 2 | `internal/regression/load.go`: read manifest dir into typed Run | 0.5 | 1 |
| 3 | `internal/regression/diff.go`: typed Diff between two Runs | 1 | 1.5 |
| 4 | `cmd/orchestrate-diff/main.go`: CLI wrapper + JSON/text report | 0.5 | 1 |
| 5 | Synthetic regression test: deliberately break fixture, diff, assert | 1 | 1.5 |
| 6 | Failure-code analytics: group / count / churn over a Diff | 1 | 1 |
| 7 | Persistent fingerprint registry: append-only history + cross-revision drift queries | 1.5 | 2 |
| 8 | M4 acceptance gate: deliberate-breakage e2e | 0.5 | 1 |
| | **Total** | **6.5** | **9.5** |

## Critical files

```
orchestrator/
  cmd/orchestrate-diff/main.go             # CLI: --before, --after, --json|--text, --register
  internal/regression/
    load.go                                # read <out>/manifest/ into Run
    diff.go                                # typed Diff between Runs
    report.go                              # JSON + text rendering
    analytics.go                           # failure-code counts + churn
    registry.go                            # persistent fingerprint history
  testdata/
    runs/                                  # checked-in synthetic before/after manifest pairs
docs/
  m4-plan.md                               # this plan
  failure-schema.md                        # gains a "diffability" section
```

## Output shape

`orchestrate-diff --before A/ --after B/ --json` produces:

```json
{
  "version": 1,
  "before": {"path": "...", "run_id": "..."},
  "after":  {"path": "...", "run_id": "..."},
  "summary": {
    "total_before": 47,
    "total_after":  47,
    "newly_failed": ["components/foo"],
    "newly_passed": [],
    "fingerprint_drifted": ["components/bar"],
    "fingerprint_stable":  43,
    "still_failed": ["components/baz"]
  },
  "details": {
    "components/foo": {
      "before": {"converted": true, "fingerprint": "abc...", "action_key": "..."},
      "after":  {"converted": false, "tier": 1, "code": "configure-failed", "message": "..."}
    },
    "components/bar": {
      "before": {"converted": true, "fingerprint": "abc..."},
      "after":  {"converted": true, "fingerprint": "xyz..."}
    }
  },
  "failure_analytics": {
    "before_codes": {"configure-failed": 1, "unsupported-target-type": 0},
    "after_codes":  {"configure-failed": 2, "unsupported-target-type": 0},
    "churned_codes": ["configure-failed"]
  }
}
```

Text rendering is optimized for terminal triage; CI dashboards consume
the JSON.

## Open questions to resolve during M4

1. **Element rename / split / merge.** When `components/foo` is split
   into `components/foo-core` + `components/foo-ui`, our diff sees
   "foo gone, two new". M4 reports it as one newly-failed + two
   newly-converted; explicit rename support is M4.x.
2. **Report retention.** `--register` appends to a fingerprint
   history; how long does that history grow before eviction? M4 ships
   unbounded; M5+ adds policy.
3. **Drift attribution.** If element B's fingerprint drifted because
   dep A's bundle changed, the diff names B as drifted but doesn't say
   "because A". Adding cross-element causal attribution is M4.x or M5.
4. **CI integration.** Should `orchestrate-diff` exit non-zero on
   newly-failed? CI wants that signal. Default: yes; opt-out via
   `--allow-regression`.

## Risks

- **Fingerprint stability across orchestrator versions.** If
  computeActionKey's input set changes (e.g. M5 adds toolchain hashes),
  every fingerprint flips even though outputs are equivalent. M4's
  registry version field guards readability; cross-version comparisons
  are explicitly unsupported until the registry knows how to project
  one schema onto another. Document the constraint.
- **Determinism.json size at distro scale.** 50K elements × ~4 output
  files × ~64-byte JSON entry per file ≈ 12MB per run. Fine for
  reasonable storage, slow for diffing in memory. M4 ships an O(n)
  diff; M5+ may switch to a content-addressed sidecar.
- **Synthetic-fixture brittleness.** The deliberate-breakage test
  edits a fixture file mid-run; CI must not see leaked state across
  jobs. Use t.Cleanup + per-test tmpdirs (already the M3a pattern).

## Acceptance

1. `orchestrate-diff --before A/ --after B/ --json` produces the
   schema above for a synthetic before/after pair.
2. Deliberately breaking one element in the fdsdk-subset fixture
   between runs produces a report listing that element under
   `newly_failed` with the expected Tier-1 code.
3. Editing a non-allowlisted .c file between runs produces a report
   with `fingerprint_drifted: []` (the shadow tree absorbs it; the
   registry agrees with the architecture).
4. Editing CMakeLists.txt between runs produces a report with the
   element under `fingerprint_drifted` and a stable fingerprint
   transition recorded if `--register` was used.
5. `make test` (no cmake) and `make test-e2e` (with cmake/bwrap) both
   pass.

## What's set up for M5 (Bazel envelope)

- The fingerprint registry becomes M5's "what's-already-converted"
  oracle for selective re-conversion.
- Failure analytics let M5's downstream Bazel build show "skip
  elements with known Tier-1 failures" rules.
- The diff tool's JSON output is the substrate for a future
  per-PR-comment bot or dashboard.
