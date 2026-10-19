# abstraction.resource contract

Remote profile: the current service methods retain `local` (explicit or
default); the general paired listener does not offer this capability. Retained
capability-specific delegation keeps its existing contract and is separate
from that listener. Held by: `idl/gen/service_test.go`
(`TestDispatcherTableCarriesRemoteProfile`) and `serve/runtime_remote_test.go`
(`TestRemoteRuntimeDescribeUsesLiveGrantAndExplicitServices`).

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

The conventions every OA contract carries are stated once in [CONVENTIONS.md](https://github.com/openabstractions/abstractions/blob/main/idl/CONVENTIONS.md); a rule here that applies one cites its `CONV-` id.

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
empty account rather than inventing one; `held` MUST sum [verified rows](https://openabstractions.github.io/reference.html#concept-resource.measurement-evidence) only.

One program holding a resource in two processes is two rows: two loaded
models were two `llama-server` processes of one image path, two rows that
moved independently. A reader that folds two hold rows into one program
loses which hold is which.

A claimed row whose holder is an HTTP engine a server adapter reads names
it `server:<name>` with an empty account: such a holder has a base URL and
no image, so there is no absolute path to give it and no account to read,
and inventing one would break the first paragraph.

A verified row whose bytes the lease book credits to a live lease (RES-L2)
MUST carry that lease's id in `lease`. A verified row with an empty `lease`
is foreign memory: a program holding the resource under no lease, which the
service measures and never asks to yield (RES-L3).

**[RES-T2] Instruments.** The instruments MUST be the service's own: the
service alone measures. On Windows the instrument MUST read `\GPU Process
Memory(*)\Total Committed` per process, and MUST read `card:0`'s capacity
from DXGI's adapter description: the largest hardware adapter's
`DedicatedVideoMemory`, `DedicatedSystemMemory` and `SharedSystemMemory`
together, the memory the operating system lets that adapter map. `card:0`'s
usage MUST come from the counter rows of that adapter's LUID alone, summed
per process, so usage and capacity describe one adapter. When DXGI names no
adapter, the instrument reads every adapter's rows and no capacity. On the owner's Radeon 8060S (128 GiB
installed, 123.6 GiB visible to Windows) that is 3.83 GiB dedicated plus
98.92 GiB shared, 102.7 GiB; the table had read the card as unknown. On Linux the instrument MUST read `/proc/<pid>/fdinfo`: each
process's `drm-resident-<region>` MUST be summed after its fds are
deduplicated by `drm-client-id`, one row per `drm-pdev`, numbered `card:<n>`
in `drm-pdev` order; a second region carried in `detail` is a second figure
beside the first, `vram` and `gtt` being amdgpu's own names for it. On
macOS with a Metal device `instrument` MUST read `metal-working-set`: it
measures `card:0`'s capacity (RES-L2) and no holder, so every row MUST be
claimed. Where no instrument exists, `instrument` MUST read `none` and every
row MUST be claimed.

A row below a declared size threshold (256 MiB on the Windows instrument)
MUST be left out, because every process on a shared-memory machine holds a
little and a table of ninety rows answers nobody's question.

`fresh` MUST read the instrument again; a reader that did not ask for one
receives the last sample while it is inside the service's age bound.
`observed` MUST be when that sample was taken, never when the reply was
written, so a reader can tell how old the answer is.

**[RES-T3] Claims.** A claimed row MUST come from a server adapter's own
model list, from a holder's `detail`, or from the lease book. A provider
registered with the service MUST contribute claimed rows only; a verified
row comes from the service's own instrument (RES-T2). A claimed
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

**[RES-T6] A table describes the answering machine.** A `table@1` reply
MUST describe the machine of the runtime that answered it. A reader that
resolves a remote runtime's table receives that runtime's `Resources()`
and rows, and MUST NOT merge them with another machine's. A lease is taken
only on the runtime of the machine whose resource it holds: the router
selects a remote server, and the remote provider takes its own lease there.
The facade test that resolves a second runtime's table is under
[Not built](#not-built).

## [Leases](https://openabstractions.github.io/reference.html#concept-resource.leases)

**[RES-L1] Admission.** `Acquire` MUST decide `abstraction.resource/hold` on
the resource for the bound caller before reading the table. A program with
no rule MUST read `not_permitted`; a decision point that cannot answer MUST
read `unavailable`, never a permit.

The additive `AcquireFor(subject, resource, amount, wait_ms)` uses the same
admission rule for an authenticated job owner asserted by an explicitly
designated native enforcer. The receiver MUST require the enforcer and the
subject to belong to its owning account and MUST recheck the native caller.
The serialized Rights Subject carries no proof. A normal application cannot
assert another owner; the installed runtime designates only its own download
runner process for `hold` on `awake`. That runner takes the original
account/program from the operation's private authenticated journal. Script-host
rules retain their existing subject and default-exclusion semantics.

The Book records the original owner. It privately retains the acquiring native
controller, which may renew/release only its own delegated leases through the
unchanged methods while its designation remains valid. Permission rechecks,
revocation and expiry are decided for the original owner. No implicit grant is
made, and a missing authenticated owner never becomes the runtime's subject.

**[RES-L2] Order, arbitration and outcome.** A request larger than the pool
MUST read `insufficient` before any holder is asked; a machine with no pool
MUST never read it. A request no larger than what is free MUST be `acquired`
with an empty `asked`: the service arbitrates scarcity and sends no yield
request where there is none.

Otherwise the service MUST plan the eviction before it asks anybody
(RES-L9) and send holders a yield request in this order: the planned
leases oldest `since` first, then it stops. Each yield request MUST be recorded in `asked` with its answer and
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

On Windows the instrument binds a child through a measurement of that exact
process instead (`instrument.ChildUsage`): it holds a handle on the bound
child, which keeps its PID from reuse, checks the child's creation time
against the bound identity, reads `D3DKMTQueryVideoMemoryInfo`'s
`CurrentUsage` for both memory segment groups on every adapter
`D3DKMTEnumAdapters2` lists, and checks the child still runs. A failed
query, linked physical adapters, a changed identity or an exit withholds
the whole figure. With a figure `x` for the child and the PDH row `y` of
its PID, the book leaves `y` charged, adds `max(0, x - y)` to what is held,
credits `x` once to the lease's subject, and gives the PDH row no ordinary
program credit. The global charge is then at least the larger figure, and
the engine's occupancy counts once instead of as its PDH row and its full
reservation. Before this, a loaded chat model took twice its size from the
owner's card and no picture model fit beside it. A PDH row alone still
proves no process generation and credits nothing by its PID.

The engine of a Windows binding is the bound child and each of its direct
children that the instrument shows holding the resource, runs as the
lease's account, started no earlier than the bound child, and runs an image
inside the engine's folder. The engine's folder is the bound image's
folder, or, for a venv launcher in a folder beside `pyvenv.cfg`, the folder
holding the venv. An engine whose folder would be a volume root has none,
and only its bound child is its own. The book measures each engine process as it measures the
bound child. When the measurement fails while the book's fresh inspection
still finds that process generation at the PID, the PDH row of that PID
credits the lease's subject, and the engine is charged as its lease or its
rows, whichever is larger, never both. A PID whose process is gone or
changed keeps the full reservation beside its row. The OA PyTorch engine
binds its venv launcher, and the interpreter the launcher starts holds the
model; before this, that interpreter's memory was charged as foreign use
beside the engine's full lease.

A generation-bearing Linux sample below a bound process may credit that
ancestor's lease group when every observed parent edge has the same account
and boot identity, nonzero strictly ordered start ticks, and unchanged native
identities and parent links on reverse reinspection. The bound ancestor must
still match its lease's saved process creation identity. A bounded walk credits each sample
once: an exact direct binding keeps its owner; otherwise all proven bound
ancestors must name one subject. Conflicting subjects, unavailable or reused
identities, equal ticks, cycles or the private depth limit provide no additional
ancestry credit; existing matching program/account credit remains unchanged.
This reconciliation leaves every globally measured byte charged
and changes no binding authority or public holder row. Unsupported instruments
retain the conservative behavior above.

**Capacity is one figure.** `capacity` in `Holders` MUST be the pool the
book admits against, by the rule below, so every reader of this machine's
memory (the lease book, the model catalogue's fit rule, the runtime's
context plan, a provider reading the table over the wire, the Panel) takes
one figure and computes none of its own. `capacity` MUST NOT read 0 for
`card:<n>` or `memory` where the machine states its memory.

Capacity MUST come from the service's own instrument and from the machine's
own memory figure. A claimed row, a server's survey, a holder's `detail` and an
engine's own report of free or total memory MUST NOT set `capacity` or the
pool: the 2026-09-05 measurement saw `hipMemGetInfo` and ComfyUI's
`/system_stats` report 78.0 of 78.19 GiB free while `llama-server` held
22.8 GB.

The pool MUST be the instrument's `capacity` where it has one. The
instrument declares which resource each resource's bytes are carved from;
resources carved from one resource are one pool. A lease on any of them
MUST count against a request for any other, and the yield requests of such
a request MUST include those leases, each asked about its own resource.
Only leases are counted across the pool: measured holds are counted per
resource, so bytes the instrument measures on a sibling resource do not
reduce a request's free.
Where the pool's resource reports no capacity and that resource is
`memory`, the service MUST read the machine's physical memory as `memory`'s
pool, which is what the 2026-09-22 measurement found a shared-memory
adapter's segment to come from. A card carved from such memory with no
figure of its own MUST read that memory less the system's reserve: an
eighth of memory and at least 4 GiB, and half of memory below 8 GiB. A
resource that is its own pool and has no capacity has no pool, and a
request there MUST ask its holders. A card the instrument cannot read
at all (a basic display adapter, a virtual machine's) MUST read as carved
from `memory`. An acquire there answers `unavailable` while the card cannot
be read; Fit reads the pool.

The reading comes from the per-user service's own interactive session:
the Windows GPU counters are unreadable from session 0, which is one reason
the service runs per user. The service MUST NOT read the WDDM per-process
video memory budget (`QueryVideoMemoryInfo`) as capacity: that budget is
the operating system's figure for one process, it moves under pressure, and
the service holds no adapter. The adapter description DXGI states is
the operating system's fixed figure for the adapter and is read instead.
Every instrument in this release declares
each `card:<n>` carved from `memory`. On macOS with a Metal device,
`card:0`'s capacity MUST be the device's recommended working-set size
(`MTLDevice.recommendedMaxWorkingSetSize`), the system's own figure for the
memory Metal keeps resident: the first device that is neither low-power nor
removable, which is the discrete card on an Intel Mac. That instrument is
`metal-working-set` and measures no holds (RES-T2). The Windows instrument
reports the adapter's DXGI figure. The Linux instrument and `none` report
no adapter capacity, so a card there reads memory less the reserve. On
every platform card and memory leases count against each other.
The instrument named `fixed` reports the capacities its owner sets; tests
and the conformance driver measure through it, and a running service never
does.

An answer in `asked` MUST be `yielded`, `refused` or `unanswered`. A refusal
MUST carry its reason after `": "`, whether the reason is the server's own
words or the rule that refused.

`wait_ms` (0..120000) MUST be clamped to the call's own connection deadline;
the service raises that deadline to `MaxAcquireWait` plus its own margin
whenever the lease book is served, so a wait the contract allows is never
cut by a shorter transport budget. Two holders asked in sequence, each
taking the full settle, both receive an answer inside that deadline.

**[RES-L3] Yield.** A lease holder yields through `Answer`. Its `yielded`
MUST be confirmed against the instrument before the asker is told.

Each `YieldRequest` MUST name the lease being asked back. A lease holder's
`yielded` answer MUST concern that lease; freeing a different lease of the
same resource does not answer the request. The holder MAY release its lease
after freeing the resource and before answering; the book ends any still-live
named lease after it confirms the instrument's fall. A yield MUST NOT stop
a component while a request uses it: the provider kit's holder waits for a
moment no request uses the component, then stops it and answers `yielded`.
A request uses a component from its admission until its call returns; a
reply kept afterwards only for observation does not use it. A holder that
counts its requests in flight (`Options.InFlight`) holds back each new
admission while the yield waits (`Holder.Pass`), so the component yields at
its next request boundary, and the held-back request then loads it again
at the size that fits (RES-L9). A holder busy with a request MUST answer
`refused` with exactly the reason `component has an active request`, and an
asker MUST treat that reason as "ask again later". The provider kit's holder
gives that answer to a request still running after its bound (`YieldWait`,
half the holder's acquire wait by default). The provider kit's acquire asks
again, up to ten more times while its caller waits, when every refusal it
received is that one, so a long turn delays a load that needs its bytes and
does not fail it. Each request is answered on its own, so a wait on one
component delays no other.

An integrated app, one that runs OA's integration, joins as a lease holder.
The rule `abstraction.resource/yield` on the holder's program is the
person's switch for that app's leases. Its resource is the subject the lease
names: an absolute image path or a package family. The service MUST decide
it for its own program before it plans or asks, once per holder program in
each `Acquire`. Only an evaluated deny keeps the app's memory: the service
MUST NOT ask that holder, and `asked` records it as
`refused: the person keeps <Program>'s memory`. The plan then covers the
shortfall from the other leases, and when they cannot, nobody is asked and
the asker reads `holders_refused`. No rule, and an outcome the rights
service cannot give, ask the holder as before: the switch is on by default and the
composition writes no rule for it. An asker's own leases are its own to ask
back, and the switch never keeps them.

An app without OA's integration, such as a stock model server, is measured
as foreign memory (RES-T1) and never driven: the service sends it nothing
and asks nobody on its behalf. A load that foreign memory blocks reads
`insufficient`, and the provider kit's refusal names the program holding
the most of it (RES-L9).

A yield frees the bytes the resource's total falls by: a holder's engine
can run in processes no row credits to its lease.

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

While any `Acquire` of a resource in the same pool is in its ask loop, `AcquireFree`
MUST read `held`, even when the request fits what is free. The bytes a
yield frees belong to the acquire that asked for them, and the lowest
standing never takes them. The book counts, per pool and under its
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

The additive Go `Book.RecheckChecked` reports unavailable decisions to checked
rights-change composition while preserving their leases. The published void
`Recheck` and its periodic outage behavior remain compatible. Cancellation
and per-pass deadline expiry are not refusal decisions.

A connection-owned hold is ended by the service that owns its connection.
The provider kit's holder reads a refused renewal as an ended lease and unloads
the component that used it. The holder's next acquire is decided, and asked,
as a first one is.

**[RES-L8] A waiting acquire names its holder.** Resource registers the
family `card` in job's waiting-word catalogue (abstraction-job JOB-A15).
While an `Acquire` of a card waits on a yield request, the service MUST
record `card:held_by:<program>` for that asker and pool, naming the holder
asked last: a lease's program as the table names it, verified or claimed.
`<program>` is formed from that name:

- an image path gives its base name, with Windows' `.exe` removed;
- a package family, with or without `msix:`, gives its package name
  without the publisher id.

It is lowercased, each character outside `[a-z0-9_.-]` is written `_`, and
it is cut so the word stays within 64 bytes. The word is set when a yield
request goes out. It stays while any `Acquire` of that asker on that pool
waits and is removed when the last one returns, so a release clears it
with the grant that follows, with no timer. It asks nobody a question and
sends no notification. The service reads the word in process
(`Book.Waiting`); its wire read and its relay to jobs and clients are under
[Not built](#not-built).

**[RES-L9] A load leases what it uses, at the largest size that fits.**
An engine whose memory depends on a size it chooses (a chat engine's
context) MUST lease what that load uses: its weights and the KV cache of
its context in every resident slot, with its startup buffers. A component
that can run at more than one size names them largest first
(`holder.Component.Smaller`). The holder MUST try each through the
no-yield acquire (RES-L6) and start the largest it is granted; only when
none fits does it `Acquire` the smallest, asking holders to yield as
RES-L2 orders. The engine then reports the size it loaded, never the size
it planned (modelhost MODELHOST-H10).

This is how a holder yields by shrinking. A larger load that does not fit
beside a resident one asks it to yield; the resident one yields once its
running request ends (RES-L3), and its next load takes the largest size
that fits beside the new holder. The yield never fails the asker for a
running request, and the shrunken holder keeps serving. A chat engine
first gives up resident conversation slots at its planned context, and
only then a smaller context. A holder grows back on its next load after
the other holder leaves.

A load that does not fit MUST unload only what makes it fit. Before any
yield request, the service plans the eviction: the smallest set of live
leases on the pool, the asker's own other leases among them, whose grants
cover what is short. A lease counts its grant, the least its yield frees.
When even every lease yielding leaves the load short, the service asks
nobody and the asker reads `insufficient` at once: nothing is unloaded for
a load that would fail anyway. The provider kit refuses that load
`lease:insufficient`. When the table shows foreign memory (RES-T1), the
provider's log names the program holding the most of it, by its file name
without `.exe`, in this shape: "<Program> holds 20 GB that OpenAbstractions
doesn't manage. Unload it in <Program>, or close <Program>." The app that
asked reads the generic sentence of RES-L10. Otherwise the log says that not
enough memory exists for the model even after unloading every model that
could make room. Idle components give way at once and a component in use gives
way at its next request boundary (RES-L3); the idle time only cleans up
what no load has needed. Within one provider the same rule swaps models: a
load asks its provider's own idle model to yield like any other lease.

**[RES-L10] Labels and the Memory record.** A holder MAY label its own
lease with `Label`: the words a person reads for what the lease holds, as
`<app>: <model>`. The text is one line of at most 200 bytes with no control
or format character (bidirectional overrides among them) and no line or
paragraph separator; empty clears it. A Memory line names a program with a
registered title or signed description by that name whatever its label
says, and takes a label's app part only for a program with neither, through
the same check as a file description. A label is display only. It needs no rule
beyond owning the lease, as `Renew` does, and carries no authority. A
reader shows the holder's program name for a lease with no label. The
provider kit's holder labels each lease when it is granted
(`Options.Label`, `Holder.Label`); a provider that runs work the runtime
forwards does not know the app that asked, and its label is the model
alone.

The lease audit is one JSON object per line. Three of its events are the
person's Memory record, and their fields are these:

- `label`: `at`, `lease`, `holder` (the lease's program) and `label`,
  written when a label changes.
- `freed`: `at`, `holder`, `lease` and `label` of the lease that yielded,
  `asker`, `asker_lease` (the lease the asker got, absent when it got
  none), `resource` and `amount` (the bytes the instrument confirmed),
  one per confirmed yield, written when the `Acquire` that caused it ends.
- `foreign`: `at`, `asker`, `holder` (the program holding the most memory
  no lease covers, other than the asker), `resource` and `amount` (its
  bytes), written when an `Acquire` ends `insufficient` or
  `holders_refused` while the table shows such memory.

An empty `label` is absent. Records also carry `amount` and `took_ms`
where those are zero; a reader ignores fields it does not use.

The app whose load foreign memory blocks reads "Not enough memory: another
program holds memory OpenAbstractions doesn't manage." The provider kit's
refusal carries it as `App` (`refusal.ForeignMemory`); the sentence naming
the program and its size stays in the provider's log, because another
program's holdings are what `abstraction.resource/table.read` guards
(RES-T4).

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
owns the holder's connection bridges the original program and account into
the resource book and releases its lease when the connection ends. The book
owns the platform request; the lease is connection-owned under RES-L4.

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
wake lease with no card lease beside it takes a system-required request
(VISION 2026-10-02 (1)), released with the last such lease.
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
composition supplies the card/display request (`BookOptions.KeepAwake`)
and the non-card system request (`BookOptions.KeepSystemAwake`); a book
without the respective hook records the lease and keeps nothing awake. A provider running
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
MUST stop every component of every holder it runs by that component's own
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
queued job waits. A Kernel-Power 506 begins standby for admission until its
507, whether or not the machine sleeps, while the console display is off; a GPU
job asks again before its first dispatch. A display that is on or dimmed
(`GUID_CONSOLE_DISPLAY_STATE`, read through a power setting notification) is
an awake machine, and a 506 with no 507 after it then admits the work with one
log line: standby begins only after the display turns off. Each admission reads
the display state again, and the same 506 refuses once the display is off. A display state that
cannot be read admits the work with one log line. The refusal's sentence is
"The computer is going to sleep; try again when it's awake." While a GPU job runs the provider MUST hold the wake
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
| `gpu:standby` | a refused load | The machine is in Modern Standby with its display off (RES-G5) |
| `gpu:quarantined` | a refused request | An identical request reset the driver (RES-G3) |
| `gpu:known-bad` | a refused request | Its shape reaches a value recorded as hanging the GPU (RES-G5) |

## Outcomes

| service | call | outcomes |
|---|---|---|
| `table@1` | `Resources` | `ok` with the machine's resource names |
| `table@1` | `Holders` | `ok` with the resource's rows, `capacity`, `held`, `observed`, `instrument`; `invalid` for an empty name, `unavailable` if the policy decision cannot answer |
| `leases@1` | `Acquire` | `acquired`, `insufficient`, `holders_refused`, `not_permitted`, `unavailable`, `invalid`; `forbidden` is reserved for peer refusal and does not replace a hold-rights `not_permitted` |
| `leases@1` | `Renew`/`Release`/`Label` | `ok` with `applied` true or false and the lease if one still stands |
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
[Leases](https://openabstractions.github.io/reference.html#concept-resource.leases): `wait_ms` on `Acquire` is 0..120000, clamped to the connection's own
deadline, which the service raises to `MaxAcquireWait` (120 s) plus a
ten-second margin whenever the lease book is served; `wait_ms` on `Observe`
is 0..30000. A lease not renewed within its lifetime (30 s by default) ends;
a holder's yield is confirmed within the settle bound (16 s by default)
before it reads `unanswered`. GPU faults: a reset record settles
for 10 s before a probe; an engine exit within 30 s of a reset record is the
reset's, a provider waits 10 s after an exit for that record, and a fault's
record gathers for 30 s; a quarantine lasts 10
minutes, doubles per strike up to 24 hours, and holds after three strikes.

## Divergences

The card-paired display request in RES-A1 temporarily preserves the existing
GPU path. VISION 2026-10-02 rejects display requests tied to a card lease by
convention. W1-05 (Train B) replaces this convention with the separately
governed `awake:display` level and attribution to the requesting application.
This repair adds no public level or wire version.

The composed deprecated rights host (kept) injects the resource book through its wake
factory. Published `rights.Start` and `rights.Serve` calls without that factory
retain their standalone compatibility fallback. Their connection protocol and
the local Job compatibility APIs remain pending the existing D30 disposition.

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

RES-T6 has no facade test that resolves a second runtime's table and shows
its rows unmerged with the local ones.

The Linux instrument reads no adapter capacity: amdgpu's
`mem_info_vram_total` and `mem_info_gtt_total` would give an APU's
GPU-visible share where the reserve rule now stands in. The Windows child
measurement (RES-L2) is held by unit tests and a self-process read; a loaded
engine's `CurrentUsage` beside its PDH row has not been compared. A holder
that shrank under RES-L9 grows back only on its next load, never while it
stays resident.

Both `table@1` and `leases@1` answer on the endpoint named
`resource-table-v1` (`go/client/client.go`), although `leases@1` is the
writing half; its rename to `resource-v1` waits for the wire renames of
0.8.0.

RES-L8's waiting word has no wire read. `leases@1` has no method that
returns it and `AcquireResult` is closed, so a program waiting in `Acquire`
from another process, such as the model host or ComfyUI's card helper,
cannot read it, and no job provider relays it into
`OperationSnapshot.waiting` (abstraction-job JOB-A15). Its surface, a read
on resource's next `leases` version or a progress word on a blocking
`Acquire`, is decided with that version. A holder whose image is an
interpreter, such as an application's own `python.exe`, reads as the
interpreter's name; naming it by its application is open.

GPU faults (RES-G1..G7) are consumed by the model host, the Lemonade
provider (`lemonaded`) and the PyTorch provider (`pytorchd`), each of which
attaches a Guard. A reset is applied to every holder the Guard has: the
records name no adapter, and attribution to one adapter on a machine with
two GPUs is not built. Linux and macOS have no record source; there the
Guard acts on engines' device-lost lines alone. The Panel line is carried
in the fault's log record; a Panel page that reads it is not built.
