# abstraction-resource

*For programs that hold a graphics card or memory, keep the machine awake, and
operators who need to know who holds them, and for model servers that take
or yield them. Example code is in [Read it](#read-it).*

Who holds graphics-card memory (a card, `card:0` is the first) and system
memory on this machine, with [leases](https://openabstractions.github.io/reference.html#concept-resource.lease) that reserve an amount and requests
to keep the machine awake.

The current `keepawake` command is `go/cmd/keepawake` in this repository. It
takes an `awake` lease through the resource client, renews until Ctrl-C, and
releases on Ctrl-C. The caller needs `abstraction.resource/hold` on `awake`.
The command under abstraction-rights remains a legacy compatibility entry point.

**What's new here:** Programs sharing one graphics card see each other: one
table says who holds how much, measured where the system can measure it, and a
lease asks the holders OA can reach (OA lease holders and the engines OA can
unload) to give memory back before a request is refused.

One table of every hold of a scarce resource on this machine, with the program
that holds it, the account it runs as, how much, and [whether an instrument measured that amount or its holder claimed it](https://openabstractions.github.io/reference.html#concept-resource.measurement-evidence).

    card:0  held 20.95 GiB  instrument windows-gpu-counters
    verified  …\llama.cpp-win-x86_64-vulkan-avx2-2.40.0\llama-server.exe  17.29 GiB
    verified  C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe  1.88 GiB
    verified  dwm                                                          1.24 GiB
    claimed   host:lmstudio   detail=gemma-4-26b-a4b-it-ultra-uncensored-heretic

Every model runtime on this machine reports its own memory and calls the card
empty while another process holds 22 GB of it. Three instruments gave three
answers at one instant and two were wrong by tens of gigabytes
(`research/resources/MEASUREMENT-2026-09-22.md`). The table is the one place
that answers the whole device.

## What it is

The service holds resources, not models. A resource is named `card:<n>` for an
accelerator's memory, `memory` for system memory, or `awake` for requests to keep the machine awake.
On an APU `card:<n>` and `memory` report the same bytes, and a reader never
sums them.

`abstraction.resource/table@1` is read-only. `Resources()` names what this
machine can report; `Holders(resource, fresh)` returns the rows. `fresh` reads
the instrument again; otherwise the last sample within its age bound stands.

A **verified** row is a process an instrument measured, attributed to a program
and an account through the same evidence rights uses: the absolute image path
read through a process handle, and the process token's owner. A process this
account cannot open keeps its process image name and an empty account.

A **claimed** row is what a holder or its server adapter says it holds. LM
Studio reports which model is loaded and no byte figure at all, on any
endpoint, so a resident model is a claim with a `detail` and no amount. A
claimed row's amount is 0 unless the row is a lease, in which case it is the
grant the service wrote. `held` sums the verified rows only.

`capacity` is 0 where the instrument cannot say, which is the case on a
shared-memory adapter whose pool is the machine's own RAM.

## [Leases](https://openabstractions.github.io/reference.html#concept-resource.leases)

[`abstraction.resource/leases@1`](https://openabstractions.github.io/reference.html#concept-resource.leases) is the writing side of the same service, on
the same endpoint. `Acquire(resource, amount, wait_ms)` decides
`abstraction.resource/hold` on the resource, reads the table, and sends the
holders yield requests in order: the leases on that resource, oldest `since`
first. It returns a [lease](https://openabstractions.github.io/reference.html#concept-resource.lease) or a
typed refusal, and every holder it asked, with what each answered and how
long it took.

    card:0  108.31 GiB  acquired  lease lease-1
      HOLDER                    FREED      ANSWER   TOOK
      C:\oa\modelhostd.exe      17.31 GiB  yielded  7.234s

An app that runs OA's integration (OA's own engines, or an app with OA's
node pack and connector) joins as a lease holder. An app without it (a
stock model server) is **foreign memory**: a verified row with no `lease`,
measured and never driven. A load that foreign memory blocks is refused;
the app that asked reads "Not enough memory: another program holds memory
OpenAbstractions doesn't manage.", and the provider's log names the program:
"<Program> holds 20 GB that OpenAbstractions doesn't manage. Unload it in
<Program>, or close <Program>."

A holder that implements the contract calls `Observe` for the yield requests
addressed to it and `Answer` to say `yielded` or `refused(reason)`. Its
`yielded` is confirmed against the instrument too.

These reservations live in the runtime's state, so a restart keeps them, and one nobody
renews by `renew_by` ends. Every ask, yield and refusal is a record with the
asker, the holder, the bytes and the time, read with
`openabstractions resources audit`.

A holder labels its own lease with `Label`, as `<app>: <model>`; the provider
kit's holder does it at each grant. The audit's `label`, `freed` and
`foreign` records are the person's Memory record: who labelled what, which
lease gave how many bytes to whom, and which program outside OA held the
memory a refused load needed (CONTRACT.md RES-L10).

A [lease](https://openabstractions.github.io/reference.html#concept-resource.lease) of `awake` acquired through `Acquire` lives until released or its
`renew_by` deadline passes. Its rule is `abstraction.resource/hold` on
`awake`. The service holds the platform wake request for these acquired
leases while the same holder also holds a card lease.

A service that owns the holder's connection can instead keep a wake request
for that connection's lifetime and record the hold in the resource table
(RES-A1). The owning service releases the request when the holder closes,
exits or is killed. A table record alone does not prove a platform request:
the composition must supply the native wake mechanism.

## What it is not

It holds nothing, loads nothing and evicts nothing of its own. Reading the
table changes the machine in no way. `leases@1` asks a holder to yield and
records the answer; it kills nothing, boosts nothing and schedules no compute.

It is not a model catalogue. Which models exist here and which server serves
them is `abstraction.router/router@1`, which reads this table for its
residency answer rather than measuring anything itself.

## Words

| word | meaning |
|---|---|
| **resource** | a scarce thing with a name: `card:0`, `memory`, `awake` |
| **holder** | the program that holds a resource; one row of the table is one hold, and a program in two processes is two rows |
| **verified** | an instrument measured this amount for this process |
| **claimed** | the holder or its adapter asserted this hold; a claimed row's amount is 0 unless the row is a lease, in which case it is the grant the service wrote |
| **instrument** | what measured: `windows-gpu-counters`, `linux-fdinfo`, `metal-working-set` (capacity only), or `none` |
| **held** | the sum of the verified amounts, and nothing else |
| **lease** | a grant of a resource to a program, asked back on demand |
| **foreign memory** | a verified row no lease covers: a program without OA's integration, measured and never asked to yield |

[CONTRACT.md](CONTRACT.md) holds the rules: RES-T1 to RES-T6 for the table,
RES-L1 to RES-L8 for the leases, RES-A1 for the wake lease and RES-G1 to
RES-G7 for GPU faults.

## Rights

Reading other programs' rows needs `abstraction.resource/table.read` on
resource `account`. A program with no rule reads its own rows, which is the
rule and not an omission: an application may always see what it holds.
`capacity`, `held` and `observed` describe the machine and name no program, so
every caller reads them.

Acquiring needs `abstraction.resource/hold` on the resource asked for. A
program with no rule is `not_permitted` before the table is read, and a
decision point that cannot answer is `unavailable`, never a permit.

Asking an app's lease back is decided as `abstraction.resource/yield` on
that app's program, held by the service. It is on by default with no rule;
denying it is how a person keeps one app's memory where it is.

## The instruments

**Windows.** `\GPU Process Memory(*)\Total Committed`, one figure per process,
per adapter LUID, summed over the LUIDs a process touches. The 2026-09-22 APU
measurement found this counter avoided double-counting Dedicated Usage. A later
read found `Total Committed` differed from `Local Usage + Non Local Usage` on
the same APU; the instrument uses the reported counter directly and makes no
cross-counter identity claim. It is read twice per query: the first collect
after opening a fresh query enumerates instances and returns partial data.
One such first read reported the process holding a 24 GB model as holding
2.81 GB. Rows under 256 MiB are left out,
because every desktop process on a shared-memory machine holds a little and a
table of ninety rows answers nobody's question.

**Linux.** `/proc/<pid>/fdinfo`, walked per process for the files this
account can read. Each process's `drm-resident-vram` is summed after its fds
are deduplicated by `drm-client-id`; each `drm-pdev` numbers a `card:<n>` in
`drm-pdev` order, and `drm-resident-gtt` — an APU's second figure, since its
VRAM is a carve-out of system memory — travels in `detail`. A process whose
`/proc/<pid>/exe` this account cannot read keeps its `comm` and an empty
account, the same fallback the Windows counters take for a process handle
that will not open. The same 256 MiB row threshold applies.

Checked on this machine's WSL (`wsl -- ls /dev/dri`): the device does not
exist, so there is no DRM fdinfo here to read live; the instrument is
untested against a running kernel and stands on the synthetic-tree tests in
`go/instrument/fdinfo_test.go` alone.

**macOS.** `metal-working-set`. It measures no holder, so every row is
claimed. `card:0`'s capacity is the Metal device's recommended working-set size
(`MTLDevice.recommendedMaxWorkingSetSize`), read through the in-house
Objective-C binding in `go/instrument/metal_native_darwin.m`. On an M5 Pro
with 48 GiB it is 40,200,896,512 bytes. The card stays carved from memory,
so card and memory leases count against each other. A build without cgo
reads no working set and the card's pool is the machine's memory.

**Everywhere else.** `none`. Every row is then claimed, and no number is
invented.

## Read it

Install the runtime first: https://openabstractions.org/adopt.html

    openabstractions resources
    openabstractions resources --fresh --json card:0

```go
package main

import (
	"context"
	"log"

	facade "github.com/openabstractions/abstraction-facade/go"
)

func main() {
	ctx := context.Background()
	table, err := facade.Discover().ResolveResourceTable(ctx, facade.Requirements{})
	if err != nil {
		log.Fatal(err)
	}
	state, err := table.HoldersContext(ctx, "card:0", false)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(state.Held, len(state.Holders))
}
```

Bindings for Go, Python, C++, Rust and JavaScript are generated from
[resource.thrift](resource.thrift) by `scripts/generate.sh`; the schema page is
[abstraction.resource.schema.html](abstraction.resource.schema.html).

The shared `rights.Subject` used by `AcquireFor` comes from the published Rights
packages: Go `github.com/openabstractions/abstraction-rights/go`, Python
`abstraction-rights-protocol`, JavaScript `@openabstractions/rights`, Rust
`abstraction-rights-api`, and C++ CMake package `abstraction_rights` with target
`abstraction::rights_client`. The package manifests declare these dependencies;
the generated bindings retain the shared subject type.

## Status

**Published.** The public `abstraction-resource` repository carries the Go
module's release tags; `abstraction-provider-kit` requires `go v0.2.1`, and
`go get github.com/openabstractions/abstraction-resource/go@latest` fetches the
newest. The contracts began as drafts of 2026-09-22.

<!-- BEGIN GENERATED PERMISSION DEFINITIONS -->

## Permission definitions

Generated from the existing compiled catalogue. [RIGHTS.json](RIGHTS.json) contains the complete action/profile records, disclosure, lifecycle, first-use behavior, selector support and finite bundles.

This reference records compiled receiving support; RIGHTS.json names the compilation context. A connected runtime advertises current availability and selector support. These definitions supply no grant.

| Action | Resource profile | Owning operations | First-use targets | Compiled receiver |
|---|---|---|---|---|
| `abstraction.resource/hold` | `abstraction.rights/legacy-exact-string@1` | `abstraction.resource/leases@1#Acquire`, `abstraction.resource/leases@1#AcquireFree` | `none` | served in compiled context |
| `abstraction.resource/table.read` | `abstraction.rights/legacy-exact-string@1` | `abstraction.resource/table@1#Holders` | `none` | served in compiled context |
| `abstraction.resource/yield` | `abstraction.rights/legacy-exact-string@1` | `abstraction.resource/leases@1#Acquire` | `none` | served in compiled context |

<!-- END GENERATED PERMISSION DEFINITIONS -->
