# abstraction.resource contract

Binds: `resource.thrift`

`resource.thrift` defines `abstraction.resource/table@1`, the read-only table
of who holds a scarce resource on this machine, and `abstraction.resource/leases@1`,
the writing side on the same endpoint that asks holders to yield. The service
holds resources, not models: a resource is named `card:<n>` for an
accelerator's memory, `memory` for system memory, or `awake` for the wake
lease, and a reader never sums `card:<n>` and `memory` on a machine whose
card is system memory. [README.md](README.md) holds the walkthrough: reading
the table and taking a lease.

## Reading this page

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY" and "OPTIONAL" in this page are to be
interpreted as described in RFC 2119 and RFC 8174, when, and only when, they
appear in all capitals, as shown here.

A rule id such as `RES-T1` is declared once, in bold brackets before its
title, at the head of the rule it names; tests and refusals cite it the same
way. A retired id is never reused; [HISTORY.md](HISTORY.md) keeps it with the
release it left. `HISTORY.md` also carries this contract's earlier drafts and
what was measured, linked from here and linking back.

| letter | topic |
|---|---|
| T | table (`table@1`) |
| L | leases (`leases@1`) |
| A | the wake lease (`awake`) — this contract's one pre-existing use of the letter, kept from `RES-A1` for continuity (S3) |
| G | GPU faults: a driver reset, standby and the provider kit's `gpufault` package |

`A` (admission) and `E` (error and outcome) are otherwise reserved with one
meaning in every contract (S12) and are not separately declared here: every
rule below states its own admission and outcome inline, the same as
`abstraction-storage/CONTRACT.md`. `X` (extension) is unused.

**Prose-to-wire.** The word below is the decided name
(`research/vocabulary/DECISION.md` D11, D88); the wire still uses the name on
the right until the release named ships (`research/vocabulary/RENAME-PLAN.md`
§4), and the stored-policy reader accepts both names for that release.

| decided name | current wire name, until it ships |
|---|---|
| `server:<name>` (resource) | `host:<name>` — first release |

## Table

**[RES-T1] Attribution.** `Holders` MUST report every hold the instrument
sees, attributed to a program and account through the same evidence rights
uses. A row the platform cannot attribute MUST keep its process image and an
empty account rather than inventing one; `held` MUST sum verified rows only.

One program holding a resource in two processes is two rows: two loaded
models were two `llama-server` processes of one image path, two rows that
moved independently. A reader that folds two hold rows into one program
loses which hold is which.

A claimed row whose holder is an attached HTTP engine names it
`server:<name>` with an empty account: such a holder has a base URL and no
image, so there is no absolute path to give it and no account to read, and
inventing one would break the first paragraph.

**[RES-T2] Instruments.** On Windows the instrument MUST read `\GPU Process
Memory(*)\Total Committed` per process, summed over the adapter LUIDs it
touches. On Linux the instrument MUST read `/proc/<pid>/fdinfo`: each
process's `drm-resident-<region>` MUST be summed after its fds are
deduplicated by `drm-client-id`, one row per `drm-pdev`, numbered `card:<n>`
in `drm-pdev` order; a second region carried in `detail` is a second figure
beside the first, `vram` and `gtt` being amdgpu's own names for it. Where
neither instrument exists, `instrument` MUST read `none` and every row MUST
be claimed.

A row below a declared size threshold (256 MiB on the Windows instrument)
MUST be left out, because every process on a shared-memory machine holds a
little and a table of ninety rows answers nobody's question.

`fresh` MUST read the instrument again; a reader that did not ask for one
receives the last sample while it is inside the service's age bound.
`observed` MUST be when that sample was taken, never when the reply was
written, so a reader can tell how old the answer is.

**[RES-T3] Claims.** A claimed row MUST come from a server adapter's own
model list, from a holder's `detail`, or from the lease book. A claimed
row's amount is 0 unless the row is a lease, in which case it is the grant
the service wrote; the adapter's size estimate goes in `detail` instead. A
server that is not answering MUST claim nothing: its last model list is not
evidence that it still holds anything.

`held` still sums the verified rows alone, so a grant is never added to the
bytes an instrument measured, and a holder that has occupied its grant is
one verified row and one claimed row of the same hold seen twice (RES-T5).

A `server:<name>` row's evidence MUST be the router's last survey of that
server, an answer the router keeps until asked again; `fresh` on `Holders`
MUST ask the router for a new survey before it reads claims, so a row a
yield already cleared is absent from a fresh read the moment the survey
confirms it gone. A caller that does not ask for fresh keeps a `server:` row
from the last survey for up to the table's own age bound.

Ten live `Router.Survey()` calls against LM Studio on the machine that
measured this ran 3.1-16.6ms each, mean 7.7ms, inside that age bound and
inside the connection deadline RES-L2 states (commit `15f1dc98`).

**[RES-T4] Own rows always readable.** `table.read` on resource `account`
MUST gate reading other programs' rows; a program MUST always see its own. A
caller the decision refuses MUST receive the rows of its own program, never
a refusal, because an application may always see what it holds.

`capacity`, `held`, `observed` and `instrument` describe the machine and
name no program, so every caller MUST read them unchanged.

A decision that cannot be obtained MUST be refused with `policy_unavailable`,
neither a permit nor a narrowing.

**[RES-T5] Never summed.** A reader MUST NOT sum two resources, and MUST NOT
sum a verified row with a claimed one. On an APU `card:<n>` and `memory`
report the same bytes; a claimed row's holder and the process the instrument
measured are the same hold seen twice, once by the server and once by the
counter. `held` MUST be the only total the service computes, and MUST be the
verified rows alone.

## Leases

**[RES-L1] Admission.** `Acquire` MUST decide `abstraction.resource/hold` on
the resource for the bound caller before reading the table. A program with
no rule MUST read `not_permitted`; a decision point that cannot answer MUST
read `unavailable`, never a permit.

**[RES-L2] Order, arbitration and outcome.** A request larger than the pool
MUST read `insufficient` before any holder is asked; a machine with no pool
MUST never read it. A request no larger than what is free MUST be `acquired`
with an empty `asked`: the service arbitrates scarcity and sends no yield
request where there is none.

Otherwise the service MUST send holders a yield request in this order:
attached holders idle longest first, then leases oldest `since` first, then
it stops. Each yield request MUST be recorded in `asked` with its answer and
time. `holders_refused` MUST be read when the yield requests did not free
`amount`; the asker allocated nothing.

A grant MUST be decided against the bytes free at the moment it is granted:
a yield request whose freed bytes a concurrent grant already spent MUST ask
again while its deadline allows, and MUST read `holders_refused` once
nothing more can be freed.

The amount granted is an admission reservation, not a physical memory quota.
The service cannot stop a holder from using more; fresh instrument measurements
charge actual use above the reservation to the pool.

Free is the resource's pool, less what the instrument measured held, less
the unoccupied part of every live lease, including earlier leases of the
asking holder. The service groups leases by program and account, then
credits matching verified bytes once against that group's combined grants.
A row without a matching program and account receives no credit against a
grant. Within a matched group, a grant counts once and its measured bytes
are credited once.

A lease holder may bind one directly spawned process to its own lease through
`BindProcess`. The service accepts the association only with a rechecked
caller, a matching parent creation identity and account, and a child whose
parent and account match. One child process identity can bind to only one live
lease. The instrument must carry that child's creation identity with its
sample, and the service must recheck that identity before crediting the
measured bytes to the lease holder's group. A missing, stale, unsupported or
unverifiable identity keeps the full lease reservation in addition to the
measured row; `BindProcess` reports `unverifiable` instead of promising
credit. Linux fdinfo can carry boot ID and raw process start ticks while a
pidfd pins the process across the read. Windows PDH GPU rows name a PID but
give no process creation identity for that measured instance, so that
instrument does not credit child bytes to leases. The public holder table
does not disclose process IDs or creation identities. When measured use
exceeds the admitted estimate, the full measured amount remains charged.
On Windows, the same child's use can therefore appear in both the PDH global
measurement and its full reservation, conservatively reducing reported free
space. Credit requires a generation-bound child measurement and a proven way
to reconcile its amount with the global PDH charge. A live process handle
around a PID-only PDH collection does not itself identify that row's process
generation.

The pool MUST be the instrument's `capacity` where it has one. Where the
instrument reports none — a shared-memory adapter whose segment is carved
from system RAM — the service MUST read the machine's own memory as the
pool for `card:<n>` and `memory`, which is what the 2026-09-22 measurement
found that segment to come from. `capacity` in the table stays what the
instrument measured; a machine that will say neither has no pool, and a
request there MUST ask its holders.

Idleness MUST be the time since a holder's own list of what it holds last
changed: it is the only idleness a table can see, no server on this machine
reports when it was last used, and a server that has held the same model
untouched longest is the one asked first.

An answer in `asked` MUST be `yielded`, `refused` or `unanswered`. A refusal
MUST carry its reason after `": "`, whether the reason is the server's own
words or the rule that refused.

`wait_ms` (0..120000) MUST be clamped to the call's own connection deadline;
the service raises that deadline to `MaxAcquireWait` plus its own margin
whenever the lease book is served, so a wait the contract allows is never
cut by a shorter transport budget. Two attached holders asked in sequence,
each taking the full settle, both receive an answer inside that deadline
rather than one answer and a budget cut.

**[RES-L3] Yield.** A lease holder yields through `Answer`; an attached
holder yields on its behalf through its server's own unload. Either way, the
answer is what the instrument shows afterwards: a lease holder's `yielded`
MUST be confirmed against the instrument before the asker is told, and an
attached holder's confirmation MUST be the same instrument check run on its
behalf.

Each `YieldRequest` MUST name the lease being asked back. A lease holder's
`yielded` answer MUST concern that lease; freeing a different lease of the
same resource does not answer the request. The holder MAY release its lease
after freeing the resource and before answering; the book ends any still-live
named lease after it confirms the instrument's fall. A holder serving an
active operation MAY answer `refused` with its reason.

Whether an attached holder may be yielded MUST be decided by the rule
`abstraction.resource/yield` on `server:<name>`, decided for the program
that would do the unloading. The holder proves nothing to this service and
answers no lease call, so the rule about its models is a rule about that
server, held by the service. A composition that serves leases writes one
permit rule per server it has a mechanism for; denying one is how a person
says an engine is not to be touched, and the refusal reaches `asked` naming
that server.

A yield frees the bytes the resource's total falls by, not one row's: an
attached holder is an HTTP engine whose weights live in whatever processes
it spawned, and no row of the table is that engine.

Confirmation MUST be bounded below by the instrument's own floor. The
Windows instrument lists no process under its 256 MiB threshold (RES-T2), so
a holder smaller than the floor unloads without a fall the counters can
show, and the asker MUST read `unanswered` with 0 bytes after the settle
bound. That word is the counter's limit and no verdict on the holder: its
lease is released, its row leaves the table, and the same unload measured
above the floor reads `yielded`. Live on 2026-09-22 the model server's 88 MB
object read `yielded` with 20,799,488 bytes once and `unanswered` once.

**[RES-L4] Lifetime.** A lease not renewed by `renew_by` MUST end; its
bytes, if still resident, MUST then be reported as a holder without a
lease.

A lock a holder keeps for the length of a load MUST NOT be needed by a
periodic duty: the provider kit's `holder` renews every lease from its grant
on, while any of its components acquires, starts, loads or stops. Renewal
MUST NOT wait on anything a load can hold. The one lock it shares with a
load is a lease's own, and that lock spans one `Renew` or one `Release` call.

When a lease ends while its component readies, the holder stops that
component, releases the lease and loads once more under a new lease; a
second ending refuses the load with an acquire failure.

The holder MUST release a component's lease only once its resource use is
confirmed to end. A component whose exit was seen but not confirmed, such as
a process tree whose supervisor could not confirm every descendant gone,
keeps its lease record through an idle or liveness pass, a yield, a driver
reset and the holder's close; it answers no yield. The holder owns this rule
for every component (`holder.ExitConfirmer`).

A lease whose lifetime is its holder's connection is the exception, and the
wake lease is the only one today (RES-A1): it never ends on a clock and
never survives a restart, because the connection that owned it did not
either. The service grants one only for a caller whose going it can see.

**[RES-L5] No compute control.** Version 1 MUST kill nothing, MUST boost
nothing and MUST schedule no compute. Amount ceilings per rule wait for a
rights rule that carries an amount.

The order such a ceiling would impose is not RES-L2's own: when it ships, a
lease queues by the amount ceiling its grant states rather than being
reordered by `since`, the shape Kubernetes names `PriorityClass` with
`preemptionPolicy: Never` — a class that queues and is never evicted.

**[RES-L6] An acquire that asks nobody.** RES-L2's acquire asks holders to
yield whenever a request does not fit what is free. `AcquireFree` is the
same acquire for a request that may take nothing from another holder:
speculative work such as inference's `preload@1` (inference INF-PL2), which
holds the lowest standing by the coordinator's decision of 2026-10-03 under
the open arbitration question of VISION 2026-09-06. It MUST decide
`abstraction.resource/hold` as RES-L1 does and read `insufficient` as
RES-L2 does. Then it MUST decide in one step under the book's lock, the
same step that grants (RES-L2's free arithmetic): `acquired` when the
request fits what is free, or when the resource has no pool and nothing
holds it, the book's live leases read in that step included; otherwise
`held` in its own `AcquireFreeOutcome`, carried by `AcquireFreeResult`.
`AcquireOutcome` and `AcquireResult` retain their published closed shapes.
It MUST NOT send a yield request, and its
`asked` is always empty. No read of the table can go stale between the
decision and the grant, so a concurrent `Acquire` either takes the bytes
first and this reads `held`, or this grants first and that one finds them
spent.

While any `Acquire` of the same resource is in its ask loop, `AcquireFree`
MUST read `held`, even when the request fits what is free. The bytes a
yield frees belong to the acquire that asked for them, and the lowest
standing never takes them. The book counts, per resource and under its
lock, the `Acquire` calls that found nothing free and are asking, from the
first ask until that call returns.

The provider kit's holder serves it as `EnsureSpeculative`. A resident
component is left as it is, and its idle clock is not touched. A missing
component loads only through `AcquireFree`, under the component lock, and
any failure, a prepare error or a refused acquire alike, refuses with no
fallback to `Ensure`; the word of a refused acquire is `lease:held`. A
resource service without `AcquireFree` answers `unknown_method`, and the
holder reads that as `lease:held` too, so a speculative load against it
loads nothing. A speculative load reserves no activity, so no yield is
refused for it. A yield request cancels a speculative load under way, and
the holder answers `yielded` once the component has stopped and the lease
is released. Once real work joins the load, the load is that work's: a
plain `Ensure` or `Begin` of the component waits on it, or the provider
reports the component active. The yield then does not cancel it and is
decided as for any holder, refused while the component is active.

**[RES-L7] A lease lives only while its rule does.** RES-L1 decides
`abstraction.resource/hold` once, at `Acquire`. The service MUST decide it
again, asking nobody, for the subject a lease names and on the lease's own
resource, a card and `awake` alike (abstraction-rights RIGHTS-R23):

- before each `Renew`: a refusal ends the lease at once. A decision that
  cannot be obtained leaves the lease standing, `applied` with its
  `renew_by` unmoved, so one transient outage costs the holder nothing; the
  lease ends at that `renew_by` only when every decision within one
  lifetime (RES-L4) fails;
- on each committed rights change, before the edit returns
  (`DecisionPolicy.OnChange`), and on the host's expiry clock, which covers a
  rule that expires: each lease its rule refuses ends, with `revoked` as the
  audit answer, and a decision that cannot be obtained ends nothing there
  (`Book.Recheck`).

A connection-owned hold is ended by the service that owns its connection.
The provider kit's holder reads a refused renewal as an ended lease and unloads
the component that used it. The holder's next acquire is decided, and asked,
as a first one is.

## Wake lease

**[RES-A1] The wake lease.** The wake lease that rights carries today is the
`awake` resource's lease; it moves under `leases@1` in the release that
ships this contract, and rights keeps the rule `May hold awake`.

What moved is the record. A wake lease is a lease of resource `awake`, a row
of this table carrying its `lease`, its `grant` and why it is held, so one
reader answers who holds the card and who holds the wake lease. Every reader
of the wake lease reads those rows: `openabstractions resources awake` from
the table, and `rights holds` through the book the rights service was
composed with.

What stayed is the lifetime. The platform request is released when the
holder closes, exits or is killed, and a request-response profile carries
one call under a bounded deadline, so a lease carried by such a call would
end at that deadline while the holder was still connected
(abstraction-rights CONTRACT.md, `go/awake_lease_test.go`). The service that
owns the holder's connection keeps the request and writes the lease here;
the lease is connection-owned under RES-L4.

The rule is `abstraction.resource/hold` on `awake`. The word `awake` that
the rights service has always taken is an alias for that rule, read by the
same decision, so the registrations a person already granted and the
readers of them keep working. A wake lease taken through `Acquire` instead
is an ordinary lease and lives by `renew_by`; a holder that wants the
connection's lifetime takes it through the service that owns its
connection.

A wake lease lives only while its rule does, as every acquired lease does
(RES-L7). Once `abstraction.resource/hold` on `awake` no longer permits a
holder (revoked, blocked or expired), the service MUST end every wake lease
that holder acquired, and the platform request with the last one. The
holder's next acquire is decided, and asked, as a first one is.

An acquired wake lease keeps the machine awake while its holder holds a
card. While any lease of `awake` granted through `Acquire` lives whose
holder also holds a lease on a `card:<n>`, the service MUST hold one
platform wake request (Windows `PowerSetRequest` with
`PowerRequestSystemRequired` and `PowerRequestDisplayRequired`; macOS
`caffeinate -di`), and MUST release it once no such pair lives: the last
wake lease released or expired, or the last card lease of its holder. A
wake lease with no card lease beside it is recorded and takes no request.
The display is required because GPU work alone takes this lease and, on a
Modern Standby machine, the display turning off is the standby entry: on
2026-10-02 the Radeon 8060S machine entered standby on its display timeout
under a system-required request, and a display-required one held it out
through every GPU run (`research/live-runs/2026-10-02.md`, "Modern
Standby"). An execution-required request alone keeps a process running in
standby but does not hold standby off: measured the same day, standby
began 30 s after a display request ended while an execution-required one
was held, and the holding process wrote a line every second for 12 minutes
with no gap; the GPU is refused in standby (RES-G4). The display request
also holds off the screen saver and the lock, which is why it lives only
beside a card lease. No system setting changes. Other holds, a download's, require the system alone. The
composition supplies the platform request (`BookOptions.KeepAwake`); a book
without one records the lease and keeps nothing awake. A provider running
GPU work takes its wake lease this way (RES-G5) and never calls the
platform itself.

## GPU faults

A GPU driver reset ends every engine on the adapter. On Windows it is a
Timeout Detection and Recovery (TDR): a GPU dispatch running past the
watchdog (about 2 s by default) makes the driver reset the adapter. On
2026-10-01 the owner's Radeon 8060S reset three times: a research D3D12
shader, PyTorch/ROCm MIOpen's live solver search during Modern Standby, and
a wrapper padding attention to 27280 tokens on the SDPA math fallback (the
last two in `research/live-runs/2026-10-01-rocm.md`, runs b and c). These
rules are owned by the provider kit's `gpufault` package beside `holder`,
because a reset is first a lease event: the engines the holder started are
gone, and their leases must be given back. A provider composes one Guard,
attaches its holders, and asks the Guard before every load and every GPU
job.

**[RES-G1] Detect.** A provider MUST read, without elevation, the platform's
reset and standby records. On Windows a reset is triggered only by a record
from a channel interactive users cannot write, with the expected provider
GUID and the kernel as its writer: `Microsoft-Windows-WerKernel/Operational`
1001 for a `WATCHDOG` or `*_WATCHDOG` component, from UserID `S-1-5-18` and
process 4. Modern Standby entry and exit are
`Microsoft-Windows-Kernel-Power` 506 and 507 in the System log, from
`S-1-5-18` with Kernel-Power's GUID. The Application log, which interactive
users and AppContainers may write (`wevtutil gl Application`: IU 0x3,
S-1-15-2-1 0x2), and the System log's `Display` 4101, which names no
provider GUID, only corroborate: their `LiveKernelEvent` 141 or 117 and
4101 records join an open fault's record and never start one. A record of
another channel, GUID or writer is no record. At start the last 506 or 507
gives the standby state, and a 506 older than the last boot
(Kernel-General 12) or with no known boot is set aside.

An engine's own device-lost line is a reset only in that engine's own
form (`providerhost/devicelost`: a line starting `ggml_vulkan: `, or
libstdc++'s terminate or `what()` line for `vk::DeviceLostError`, after any
progress bar the engine drew on that line; for modelhost's PyTorch engine,
`oa-torch-engine: device-lost: ` and a HIP error name) and only once the engine exits
or fails its readiness twice within 15 s. A marker inside caller text the
engine echoes, or a line from an engine that keeps answering, is set aside
with no fault and no quarantine strike.

An engine that exits within the window (30 s) before or after a reset
record is the reset's; the provider waits at most 10 s after an exit for a
record to arrive, so a plain crash reads `engine:lost` no later than that.
A source that cannot read the records MUST be reported and read as blind,
never as a quiet machine; it is subscribed again after a backoff of 1 s
doubling to a minute, with query errors tolerated so a missing
WerKernel channel keeps the System log, and a subscription that stands
clears the blindness. Records inside one window are one fault.

On 2026-10-01 each of the three resets wrote WerKernel 1001 first and the
Application log's LiveKernelEvent about ten seconds later; none wrote a
Display 4101.

**[RES-G2] Every engine on the adapter is lost.** On a reset the provider
MUST stop every component of every attached holder by that component's own
process handle, release its lease once the stop is confirmed, and report
the engine with `engine:gpu-reset`. A component whose stop or release
cannot be confirmed keeps its lease record, as an unload does. A load still
waiting for readiness is stopped too.

**[RES-G3] The running request fails and is quarantined.** The request that
was running when its engine was lost in a reset MUST fail with `gpu:reset`.
Its fingerprint (the model, the engine, and the request's shape: size,
length and dispatch settings, never its prompt or seed) MUST be quarantined:
an identical request is refused `gpu:quarantined` for 10 minutes after the
first strike, doubled after each further strike up to 24 hours, and held
until a person clears it after the third. The quarantine MUST outlive a
provider restart. Only the owner or the user clears it; the provider never
retries the identical request on its own. A quarantine file that cannot be
read as this version's (not JSON, another version, an entry whose key is not
its fingerprint's) MUST be moved aside beside itself as
`<file>.bad-<UTC time>`, reported in one log record, and the provider starts
with an empty quarantine; listing the quarantine names the moved file, and
clearing every entry of a damaged file moves it aside.

**[RES-G4] Reload after the driver is back.** After a reset no load MUST
start until the settle (10 s after the last reset record) has passed and the
probe finds the adapter again (`instrument.GraphicsAdapters`: the GPU adapter
counters list it). The engine's own readiness on that load is the probe's
second half. A load refused meanwhile reads `gpu:resetting`. A load is asked
twice, before its lease and again after the grant and before its engine
starts, so a reset or a standby entry that began during the lease wait
refuses the start and the grant goes back unused.

**[RES-G5] Prevent.** While the machine is in or entering Modern Standby
no new GPU load or GPU job MUST start; a load reads `gpu:standby`, and a
queued job waits. While a GPU job runs the provider MUST hold the wake
lease (RES-A1), one for all its concurrent jobs. Each engine kind carries a
dispatch row as data (`gpufault.DispatchFor`, which hands out copies): the
environment it runs with,
the arguments it always carries, and the request-shape values recorded as
hanging the GPU, which are refused `gpu:known-bad`. A row changes by adding
evidence to it.

| Engine kind | Row | Evidence |
| --- | --- | --- |
| `sdcpp` | `--diffusion-fa` always | FastWan 1280x704x121 without flash attention reached 87 GB of commit; with it, 32.8 GB (`research/live-runs/2026-10-01.md` run 3b, 3d) |
| `pytorch-rocm` | `MIOPEN_FIND_MODE=FAST`, `MIOPEN_DEBUG_CONV_DIRECT=0`, the naive solvers off (`MIOPEN_DEBUG_CONV_DIRECT_NAIVE_CONV_FWD`, `_BWD`, `_WRW` `=0`), the Winograd Fury solvers off, `TORCH_ROCM_AOTRITON_ENABLE_EXPERIMENTAL=1`; MIOpen convolution off and `seq_len` from the clip as engine fixes; `math-attention-tokens` from 27280 refused | MIOpen's live solver search in standby (run b) and Hummingbird-XT padded to 27280 tokens on the SDPA math fallback (run c) each ended in a TDR; runs b', c' and c'' with these settings passed (`research/live-runs/2026-10-01-rocm.md`); the naive solvers, timed at up to 6,667 ms per dispatch in run b's search, are off as the stack review's condition 4 asks (`research/security-reviews/pytorch-rocm-windows-2.11.0-rocm7.13.0.md`) |

**[RES-G6] Report.** Each fault MUST be one log record through OA logging
(`gpu fault`: time, source record, engines stopped, engines whose lease
stayed, fingerprints quarantined) and carry one Panel line of one sentence
naming the engines stopped and whether the running request is paused. A
fault with no engine and no request has no Panel line.

**[RES-G7] The TDR delay is the owner's.** The Windows TDR settings
(`TdrDelay`, `TdrDdiDelay`, `TdrLevel` under
`HKLM\SYSTEM\CurrentControlSet\Control\GraphicsDrivers`) are a system
setting owned by the machine's owner, and OA MUST NOT read them as
permission or change them. Raising `TdrDelay` lets a long dispatch finish
instead of resetting the adapter, at three costs: a hung GPU freezes the
display for the whole delay before Windows recovers it, a delay set too
high turns a recoverable hang into a system that needs a hard reset, and
the change needs an administrator and a restart and applies to every
program on the machine. OA prevents long dispatches through the rows of
RES-G5 and recovers through RES-G2..G4, whatever the delay.

| Word | Where | Meaning |
| --- | --- | --- |
| `gpu:reset` | the failed request | Its engine was lost in a driver reset (RES-G3) |
| `engine:gpu-reset` | the stopped engine | Why the holder stopped it (RES-G2) |
| `gpu:resetting` | a refused load | The driver reset moments ago or the adapter is not listed again (RES-G4) |
| `gpu:standby` | a refused load | The machine is in Modern Standby (RES-G5) |
| `gpu:quarantined` | a refused request | An identical request reset the driver (RES-G3) |
| `gpu:known-bad` | a refused request | Its shape reaches a value recorded as hanging the GPU (RES-G5) |

## Outcomes

| service | call | outcomes |
|---|---|---|
| `table@1` | `Resources` | `ok` with the machine's resource names |
| `table@1` | `Holders` | `ok` with the resource's rows, `capacity`, `held`, `observed`, `instrument`; `invalid` for an empty name, `unavailable` if the policy decision cannot answer |
| `leases@1` | `Acquire` | `acquired`, `insufficient`, `holders_refused`, `not_permitted`, `unavailable`, `invalid`; `forbidden` is reserved for peer refusal and does not replace a hold-rights `not_permitted` |
| `leases@1` | `Renew`/`Release` | `ok` with `applied` true or false and the lease if one still stands |
| `leases@1` | `Observe` | `ok` with a page of `YieldRequest`s; `invalid` for a wait outside its bound |
| `leases@1` | `Answer` | `ok` with `recorded` true or false; `invalid` for an unrecognized answer |
| `leases@1` | `BindProcess` | `bound`, `unverifiable`, `refused`, `invalid`; `forbidden` and `unavailable` are reserved |

The five `ok` reply envelopes contain their payload exactly for `ok`; they
reserve `forbidden`, `unavailable`, `invalid` and `unknown` as typed
refusals. A refused table-read policy still returns `ok` with the caller's
own rows under RES-T4. Calls can also refuse on the reply's error channel
when transport, caller binding, or an unexpected internal failure prevents a
typed reply. A yield request's
own answer, once recorded in `asked`, is `yielded`, `refused` (with its
reason after `": "`) or `unanswered`.

## Bounds

Table: a sample stands for up to its age bound (2 s) before `fresh` reads
the instrument again; rows below 256 MiB (Windows instrument) are left out.
Leases: `wait_ms` on `Acquire` is 0..120000, clamped to the connection's own
deadline, which the service raises to `MaxAcquireWait` (120 s) plus a
ten-second margin whenever the lease book is served; `wait_ms` on `Observe`
is 0..30000. A lease not renewed within its lifetime (30 s by default) ends;
an attached holder's yield is confirmed within the settle bound (16 s by
default) before it reads `unanswered`. GPU faults: a reset record settles
for 10 s before a probe; an engine exit within 30 s of a reset record is the
reset's, a provider waits 10 s after an exit for that record, and a fault's
record gathers for 30 s; a quarantine lasts 10
minutes, doubles per strike up to 24 hours, and holds after three strikes.

## Divergences

- **RES-L3.** A yield's confirmation is the resource's whole total falling,
  not proof that the freed bytes came from the yielded holder: a concurrent
  allocation by another process on the same machine is attributed to the
  yield. A causal, per-process confirmation is the usual expectation for an
  eviction API; this service's floor is the counter's own (RES-T2), and a
  second, more exact instrument is where that confirmation would come from.

## Not built

`abstraction.resource/leases@1` version 1 kills nothing, boosts nothing and
schedules no compute (RES-L5). Amount ceilings per rule wait for a rights
rule that carries an amount; when built, a lease queues by that ceiling
rather than being reordered by `since` (RES-L5). A model catalogue is not
this service's job: which models exist and which server serves them is
`abstraction.router/router@1`, which reads this table for its residency
answer rather than measuring anything itself. A second, more exact
instrument for confirming a yield smaller than the counter's own floor
(RES-T2, [Divergences](#divergences)) does not exist.

GPU faults (RES-G1..G7) have one consumer today, the model host. Engines a
Lemonade server manages and the PyTorch provider (TheNoise) do not yet
attach a Guard. A reset is applied to every holder the Guard has: the
records name no adapter, and attribution to one adapter on a machine with
two GPUs is not built. Linux and macOS have no record source; there the
Guard acts on engines' device-lost lines alone. The Panel line is carried
in the fault's log record; a Panel page that reads it is not built.
