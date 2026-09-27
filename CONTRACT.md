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
default) before it reads `unanswered`.

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
