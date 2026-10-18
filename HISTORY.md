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

## 2026-10-01: GPU faults and the acquired wake lease

The owner's Strix Halo (Radeon 8060S, gfx1151) reset its GPU driver three
times on 2026-10-01, each a Windows TDR: a research D3D12 GDeflate shader;
PyTorch/ROCm MIOpen's live solver search during Modern Standby (WerKernel
`WATCHDOG` and `AMD_WATCHDOG` live dumps); and AMD's Hummingbird wrapper
padding attention to 27280 tokens on the SDPA math fallback. The records
read back that evening, unelevated: WerKernel/Operational 1001 at 15:40:06,
20:41:36, 20:43:59 and 21:08:32/41 local time, each followed about ten
seconds later by an Application `LiveKernelEvent` (141, or the vendor code
a1000001 for `AMD_WATCHDOG`); no System `Display` 4101 at all; Kernel-Power
506 at 20:25:00 and 507 at 20:42:55, which places the 20:41:36 dump inside a
Modern Standby. OA had no handling for them: an OA engine ended by a reset
read `engine:lost`, the holder released only that engine's lease on its
next tick, the other engines on the adapter stayed listed, and an identical
request could run again at once.

RES-G1..RES-G7 add GPU fault handling with one owner, the provider kit's
`go/gpufault` package beside `go/holder`, since the engines a reset ends are
the holder's components and their leases must be given back. `holder` gains
`Options.Admit`, asked before every new load, and `LoseAll`, which stops
every started component by its own `Owned.Stop` (outside the component
lock, so a load waiting for readiness ends too) and releases its lease. The
Windows source subscribes to one structured query over the System,
WerKernel/Operational and Application logs through `wevtapi.dll`
(`EvtSubscribe`, `EvtNext`, `EvtRender`, `EvtQuery`), bound in-house because
`golang.org/x/sys/windows` has no wrappers for it. The probe after a reset
is the GPU adapter counters listing an adapter again
(`instrument.GraphicsAdapters`), the counter set the table already reads.

RES-A1 gains one paragraph: an acquired wake lease keeps the machine awake.
Before it, a lease of `awake` taken through `Acquire` was a row and nothing
else; the platform request lived only in rights, on the connection-owned
hold. The book now holds one platform request while any acquired wake lease
lives (`BookOptions.KeepAwake`), so a provider takes the wake lease through
leases@1 and never calls `SetThreadExecutionState` or `PowerSetRequest`
itself. The composition passes the platform request: the runtime gives its
book `job.Platform`, and writes `abstraction.resource/hold` on `awake` beside
the card rule of every declared native provider that names a card. The
`pytorch-rocm` dispatch row follows `research/live-runs/2026-10-01-rocm.md`.

RES-G7 records that the TDR delay is the owner's system setting. OA does not
change it; the dispatch rows of RES-G5 keep dispatches short instead.

The independent review of `2f63b4994` asked for revisions, applied the same
day. The Application log is writable by interactive users and AppContainers,
so its LiveKernelEvent and the GUID-less Display 4101 now only corroborate,
and a reset needs a WerKernel record from S-1-5-18, process 4, with
WerKernel's GUID (RES-G1). An engine's device-lost line counts only in that
engine's own form and once the engine is gone, since engine output echoes
caller text. A 506 older than the last boot no longer reads as standby at
start. The holder asks Admit again after the grant (RES-G4); a damaged or
newer quarantine file is moved aside instead of stopping the provider
(RES-G3); a failed record source is subscribed again with backoff and
tolerates a missing channel (RES-G1); the dispatch rows are handed out as
copies.

## 2026-10-02: the wake lease keeps the display on, and a declared card gets it

The first GPU runs through the runtime (`research/live-runs/2026-10-02.md`)
found two gaps in RES-A1. The owner's machine entered Modern Standby on its
display timeout under a system-required request (00:57:24), and a
display-required one held it out through every run; the acquired wake lease
now keeps both requests (`job.PlatformGPU`), and other holds keep the
weaker one. And modelhost's wake lease was refused because its declaration,
written with `provider add`, could name no card; the registry already wrote
`hold` on `awake` beside a declared card, and `provider add --card` now
names one (facade FAC-R3).

The review of that change asked whether `PowerRequestExecutionRequired`
would do, since the display request also holds off the screen saver and
the lock. Measured on the same machine (display timeout 300 s on AC): with
an execution-required request held and the display request released at
05:03:20, the machine entered Modern Standby at 05:03:50; the holding
process wrote a heartbeat every second for 12 minutes with no gap above
1.02 s. Execution-required keeps a process running in standby and does not
hold standby off, and the GPU is refused in standby (RES-G4), so the
display request stays. The book now holds it only while the wake lease's
holder also holds a card lease (`Book.syncAwake`), so no wake lease alone
can keep the display on; the lock trade-off is an owner question in
TODO.md.

## 2026-10-02: the PyTorch engine's device-lost form

RES-G1 names a third engine form: modelhost's OA PyTorch engine prints
`oa-torch-engine: device-lost: ` and a HIP error name when PyTorch raises a
HIP error for a lost device, then exits (`providerhost/devicelost`
`PyTorchEngine`; modelhost MODELHOST-T8). The `pytorch-rocm` dispatch row
is unchanged; modelhost applies its variables and its two engine fixes.

## 2026-10-02: renewal never waits for a load

A review of a modelhost branch found that the provider kit's `holder`
renewed each lease under its component's lock, the lock `Ensure` keeps
through `Acquire` (up to 60 s), `Start` and readiness. One slow load, such
as a first launch's file proof or a large model, stalled every renewal of
the holder; after the 30 s `DefaultLifetime` every lease had ended, and the
next renewal pass unloaded engines that were serving work as "lease ended".
A lease granted to a component that was still starting was not renewed at
all until its start returned.

The holder now renews from a registry of granted leases, under its own
lock, from the grant on. The idle and liveness pass runs beside renewal and
skips a component whose lock is taken until the next pass. A renewal the
service refuses marks its component, which unloads when its lock is free.
One renewal and one release of the same lease never overlap. RES-L4 carries
the rule. The consumers are modelhost (chat, video and PyTorch video
engines), TheNoise and the `gpufault` guard's `LoseAll`.

The review of that change left one gap: a renewal refused while a component
readied let `Ensure` return the component, and the next idle pass unloaded
it under the caller. `Ensure` now checks the lease after readiness, stops
the component, releases the lease and loads once more; a second refusal
returns an acquire `Failure` wrapping `holder.ErrLeaseEnded`. RES-L4 now
names the per-lease lock as the only one renewal shares with a load.

## 2026-10-02: one owner for a provider's Guard and its lease words

The managed Lemonade provider joined the model host under `holder` and the
`gpufault` Guard (architecture review 2026-10-02 D1), and the two copies of
the Guard composition and of the lease refusal words moved here.
`gpufault.ForProvider` composes a provider's Guard: the platform's records
on Windows, `gpu-quarantine.json` in the provider's state, one warning
record per fault and per handling error on the logger the provider passes,
and the fault's Panel line on stderr under the provider's name. The provider
passes its OA logging logger, so this module takes no logging dependency.
`holder.WordNotPermitted`, `WordInsufficient`, `WordHoldersRefused` and
`WordEnded`, read through `Failure.Word`, are the words a provider gives a
load its lease refused. Consumers: modelhostd and lemonaded compose the
Guard through `ForProvider`; the model host's `host` package and the Lemonade
provider's `card.go` take the words from `holder`. TheNoise uses `holder`
without the Guard or the words.

## 2026-10-02: an unconfirmed exit keeps the lease

The review of the Lemonade card lease found that its component read stopped
as soon as Lemonade's tree exit was seen, also when the supervisor could not
confirm every descendant gone. The holder's liveness pass, a driver reset and
a yield then released the lease while engines might still hold card memory.
The holder now owns the rule for every component: an `ExitConfirmer` whose
exit is unconfirmed is not stopped, keeps its lease record and answers no
yield, whatever its own `Stop` reported (RES-L4). Consumers: the Lemonade
provider's tree implements `ExitConfirmer` and reports its own `Stop` false
too; the model host's engines and TheNoise already read an unconfirmed exit
as alive (the model host's exit channel closes only after a confirmed exit,
TheNoise's `Stop` fails when its wait does), so the holder never releases
their leases early.

## 2026-10-03: an acquire that asks nobody

Three review rounds of inference's `preload@1` (modelhost MODELHOST-W2)
found every window a check-then-act emulation of speculative standing left
in the model host: a table read then an acquire that could ask a holder,
an ensure that reset a resident's idle clock, fallbacks to a full ensure,
and a yield refused during a preload's load. The coordinator's decision put
the standing in this layer, its owner. New rule RES-L6, which extends
RES-L2: `leases@1` gains `AcquireFree(resource, amount)`, deciding and
granting in one step under the book's lock and reading the new outcome
`held` instead of asking (`go/service/leases.go` `Book.AcquireFree`,
`go/service/leases_host.go`, `go/client/leases.go`). The holder gains
`EnsureSpeculative`: a resident is left with its idle clock untouched, a
missing component loads only through `AcquireFree` with no fallback, the
word of a refused acquire is `lease:held` (`WordHeld`), and a yield request
cancels a speculative load under way (`go/holder/holder.go`). Tests:
`go/service/leases_free_test.go` (what is free, held without asking,
insufficient, 50 raced rounds against a plain `Acquire`) and
`go/holder/speculative_test.go` (the idle clock at Idle less a second, no
fallback after eviction or a failed prepare, a yield during the load).

The fourth review round found four gaps, closed the same day. A yield
cancelled a speculative load even after a real request had joined it; the
holder now counts the `Ensure` and `Begin` calls on the component and
cancels only while none waits and the provider reports it inactive. An
`AcquireFree` could take bytes a plain `Acquire`'s yields had just freed;
the book now counts the `Acquire` calls in their ask loop per resource,
and `AcquireFree` reads `held` while any is. On a resource with no pool
the live leases are now read under the lock that grants. A service older
than RES-L6 answers `AcquireFree` with `unknown_method`, which the holder
now reads as `lease:held`. Tests: `TestASpeculativeLoadRealWorkWaitsOnIsNotCancelled`
and `TestASpeculativeLoadAgainstAnOlderServiceIsHeld` (a wire-level
dispatcher without the method) in `go/holder/speculative_test.go`;
`TestAcquireFreeReadsHeldWhileAPlainAcquireAsks` and
`TestAcquireFreeOnAResourceWithNoPoolCountsLiveLeases` in
`go/service/leases_free_test.go`. Review pending.

## 2026-10-07: a revoked wake grant ends the wake lease

The owner's rule of 2026-10-02 (VISION, wake entry, point 4) makes
keep-awake a per-application grant and revoke, and a revoke ends the live
platform request. RES-A1 tied the lease to its holder's renewal and decided
`abstraction.resource/hold` on `awake` only at acquire, so a revoked app kept
the machine awake for as long as it renewed, and the Panel's Device page
could only link to the app's permissions (TODO "Revoking wake ends the app's
keep-awake hold"). RES-A1 now states that a wake lease lives only while its
rule does. `BookOptions.Standing` decides the rule again by the subject a
lease names; `Book.RecheckWake` ends each acquired wake lease whose holder
it refuses, and the platform request with the last one. An unavailable
decision ends nothing. The host calls it on its expiry clock, which covers
an expired rule and an edit by any writer; the runtime also calls it from
the rights policy's `OnChange` (RIGHTS-R13), so a revoke ends the lease
before its reply. Connection-owned holds are ended by the rights service that
owns their connection, as before. Card leases are not rechecked. Tests:
`go/service/awake_revoke_test.go` (revoke, a second program's lease kept,
an outage kept, the next acquire refused) and the runtime's
`serve/runtime_wake_revoke_test.go` (revoke through the decision policy and
an expired timed grant, each releasing the stand-in platform request).

Review the same day: an outage that ended nothing was unbounded, because
`Renew` extended a wake lease without deciding again, so a revoked holder
kept wake for as long as rights stayed unavailable. `Book.Renew` now decides
`hold` on `awake` again for each renewal of an acquired wake lease, fail-closed:
a refusal ends the lease at once, and an unavailable decision refuses the
renewal, so the lease ends at its `renew_by`, one lifetime (30 s) at most.
Test: `TestARevokedWakeLeaseEndsWithinOneLifetimeThroughAnOutage` in
`go/service/awake_revoke_test.go`.

## 2026-10-07: every acquired lease lives only while its rule does

The survey after the wake fix found that card leases renewed without a
decision, so a provider whose `hold` on `card:<n>` was revoked or blocked
kept its lease, and the card, for as long as it renewed (TODO "Live grants
outlive revocation"). RES-L7 states the rule for every acquired lease, and
RES-A1 now refers to it. The wake-only `wakeStanding` and `RecheckWake`
became `standingOf` and `Book.Recheck` (`go/service/standing.go`), which
decide `hold` again on each lease's own resource. `Renew` decides before it
extends any acquired lease, fail-closed; `Recheck` runs on the host's
expiry clock and on each committed rights change. The outage test now also
shows the card lease refused its renewals and ended within one lifetime.
Tests: `go/service/standing_test.go` (revoke ends the card lease through
`Recheck`, another program's lease stays, an outage ends nothing at once,
the next renewal and acquire are refused; a renewal alone after a revoke
ends the lease) and the runtime's `serve/runtime_standing_test.go` (Block
through the decision policy ends the card lease before it returns).

Review the same day: a single unavailable decision returned `applied: false`,
which the provider kit's holder reads as an ended lease, so one slow
decision read during a model load would have unloaded the engine. `Renew`
now keeps the lease on an unavailable decision, `applied` with `renew_by`
unmoved, and the lease ends at that `renew_by` only when every decision in
the lifetime fails (RES-L7). `gpufault`'s wake renewal reads the same
answer and keeps its lease. The runtime now removes its rights change hook
when the resources close. Tests: `go/holder/standing_outage_test.go` (one
outage then a decision keeps the component loaded; an outage through a
whole lifetime unloads it; fails with the old answer) and the outage test in
`go/service/awake_revoke_test.go` (no renew_by moves during the outage).
