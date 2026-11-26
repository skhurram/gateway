# How this was built

This project is a learning and portfolio project. Most of the code was drafted
with AI coding assistants and then reviewed, tested, and changed by the author.
This page says how that worked, what the assistants got wrong, and what the
author decided.

## Tools

- **Claude Code** (Anthropic) with Claude models, for planning, implementation,
  test writing, and review. Commits it helped write carry a
  `Co-Authored-By: Claude` trailer.
- **GitHub Copilot**, which is in the dev container and appears as a co-author
  on some commits.

Commit trailers show who helped with a commit. They do not show who decided
what, or who checked the result. The author did both.

## Process

1. **Plan.** The project was split into weekly milestones with a "done when"
   test for each one. Planning notes lived in a local folder that is not part of
   the published repository.
2. **Build in small steps.** Each change came with tests. Tests ran with
   `go test -race`, and CI runs them on every push.
3. **Adversarial review.** After a milestone, the code was read again with an
   explicit brief: find faults, do not confirm that it works. The findings were
   recorded as issues.
4. **Fix with a failing test first.** For each finding, a test was written that
   fails on the current code, then the fix made it pass. The fix for client
   disconnects and half-open trial leaks is one example; see its commit message
   on `main`.
5. **Record decisions.** Choices with real alternatives get a short note in
   [`docs/decisions/`](decisions/).
6. **Human approval for anything outward-facing.** Pushes, force pushes, history
   rewrites, issue creation, and visibility changes were approved by the author
   before they happened.

## What the author decided

- The scope, the architecture, and the milestones.
- Which findings to fix now and which to defer (for example, the trusted-proxy
  design for rate limiting is deferred).
- Every commit to `main`, every push, and the one history rewrite (below).
- The numbers in the README come from runs on the author's machine or cluster.
  The README says where each one was measured (hardware, cluster, or load
  settings).

## Where the assistants were wrong or needed correcting

These are real examples from this repository:

- **Breaker trial leak.** A half-open circuit breaker could reserve a trial
  request and never release it, leaving an upstream out of rotation. Found in
  review, fixed, with a test.
- **Client disconnects counted as backend failures.** A client that hung up
  mid-request was recorded as an upstream failure, so one client could open a
  breaker on a healthy backend. Found in review, fixed, with a test.
- **Review is not proof.** The review found real problems, but it was a
  reading, not a test run. Findings are listed as "verify" until a test or a
  live run confirms them.

## History rewrite

The repository history was rewritten once, before publication. The rewrite
removed a development-environment URL and a flag that exposed a port on all
interfaces, and it removed the planning folder from history. The commit messages
and the code changes that remain are unchanged.

## Known gaps

Open issues are in the [issue tracker](https://github.com/skhurram/gateway/issues).
Parts of the repository have had less review than others (see the issues and the
README's limitations section for the current list).
