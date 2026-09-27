# History

Design records for [`CONTRACT.md`](CONTRACT.md), kept out of the rule pages
per `research/vocabulary/DECISION.md` S7.

As of 2026-09-26 the public repository exists and has no release tags. The
dated draft quotation below records the publication state on 2026-09-22.

## 2026-09-22: draft

Before the contract shape below, this page opened with a draft note and a
publication note, kept here word for word:

"Draft of 2026-09-22, amended three times the same day as `table@1` and
`leases@1` were built and run live against measured hardware and the LM
Studio on the owner's machine (`research/resources/MEASUREMENT-2026-09-22.md`)."

"**Not yet published.** The `abstraction-resource` repository does not exist
yet; this layer publishes there, first tag `go/v0.1.0`, once the owner
creates it, after the published `identity` and `rights` modules this layer
depends on. Publication also waits on the first slice passing its
acceptance on the owner's laptop (`research/resources/PROPOSAL.md` §5). The
release that ships `leases@1` is 0.3.0."

## 2026-09-23: contract shape and the resource review

`research/reviews/resource-2026-09-23.md` ("accept with listed changes")
reviewed this page at `d146b33b` against Windows GPU process counters, Linux
DRM fdinfo, POSIX `F_SETLEASE`, Kubernetes `Lease` and preemption, and
Ollama/LM Studio/ComfyUI's own unload mechanisms. This page carried no
`Binds:` line and no `abstraction.<x>/<y>@<n>` wire name in its first twenty
lines (`scripts/check.baseline` `contract-binding` and `contract-shape`,
both cleared by this change); its ids moved from `**RES-T1.**` to
`**[RES-T1] Title.**` with a declared letter table (`research/vocabulary/DECISION.md`
S3, S12).

Six of the review's ten findings landed here. F1: `insufficient` is now a
stated rule (RES-L2), not an outcome only the wire and the code carried. F2:
a claimed row's amount is one sentence across `CONTRACT.md` (RES-T3) and the
README's Words table. F3: RES-L2 states the order the book actually uses
(`leases.go:585`, "leases oldest `since` first"), and "the order their
grants say" moves to RES-L5 as the future rule shape once an amount ceiling
exists. F5: the citation of `research/agent-history.md`, which does not
carry the ten survey timings, is replaced by commit `15f1dc98`, which does.
F6: RES-L2 now states that the service raises its own connection deadline to
`MaxAcquireWait` plus a margin whenever the lease book is served
(`go/service/host.go` `tableCallBudget`), so RES-T3 no longer claims a
thirty-second call budget the transport does not enforce. F9: RES-T2 now
names the Windows counter (`\GPU Process Memory(*)\Total Committed`) and the
amdgpu region names (`vram`, `gtt`) instead of leaving them to the README
alone. F4 (a `reference.html` glossary citation) and F10 (a change to
`abstraction-rights/CONTRACT.md`) belong to other modules' own steps in
`research/vocabulary/RENAME-PLAN.md` and are untouched here.

The same pass applied `research/vocabulary/DECISION.md`'s word choices: D11
renames the model-server sense of "host" to "server" throughout, including
the resource string `host:<name>` to `server:<name>` in prose — the wire
keeps `host:<name>` until the first release after the docs
(`research/vocabulary/RENAME-PLAN.md` §4), with the dual reader owned by
rights and lend, not this module. D14 keeps "host" for the Go `Host` type's
own package in code font and renames its prose uses to "the service". D25
states a holder as the program and a row as one hold. D48 gives `yield` one
definition: a lease holder through `Answer`, an attached holder through its
server's own unload, the answer being what the instrument shows afterwards.
D49 renames the noun "ask" to "yield request" where it names the mechanism,
leaving the plain-English verb alone. D66 renames "awake hold" to "wake
lease"; the action `abstraction.resource/hold` keeps its name.
