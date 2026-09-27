# abstraction-resource

Who holds the card. One table of every hold of a scarce resource on this
machine, with the program that holds it, the account it runs as, how much, and
whether an instrument measured that amount or its holder claimed it.

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

**Not yet tagged.** The public `abstraction-resource` repository exists, but
has no release tag as of 2026-09-26. Its planned first tag is `go/v0.1.0`,
after the published `identity` and `rights` modules this layer depends on
(`research/release-0.3.0/PLAN.md`). The contracts began as drafts of
2026-09-22 and remain subject to review before that first tag.

## What it is

The service holds resources, not models. A resource is named `card:<n>` for an
accelerator's memory, `memory` for system memory, or `awake` for the wake lease.
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

## Leases

`abstraction.resource/leases@1` is the writing side of the same service, on
the same endpoint. `Acquire(resource, amount, wait_ms)` decides
`abstraction.resource/hold` on the resource, reads the table, and sends the
holders yield requests in order: attached holders idle longest first, then
the leases on that resource, oldest `since` first. It returns a lease or a
typed refusal, and every holder it asked, with what each answered and how
long it took.

    card:0  108.31 GiB  acquired  lease lease-1
      HOLDER         FREED      ANSWER   TOOK
      host:lmstudio  17.31 GiB  yielded  7.234s

An **attached holder** predates OA: LM Studio, Ollama and ComfyUI hold the card
and answer no lease call, so the service yields them through each server's own
mechanism — LM Studio's unload endpoint, Ollama's `keep_alive: 0`, ComfyUI's
`/api/free` — and reports the bytes the instrument then shows, never what the
server replied. Whether a server may be yielded is the rule
`abstraction.resource/yield` on `server:<name>`.

A holder that implements the contract calls `Observe` for the yield requests
addressed to it and `Answer` to say `yielded` or `refused(reason)`. Its
`yielded` is confirmed against the instrument too.

Leases live in the runtime's state, so a restart keeps them, and one nobody
renews by `renew_by` ends. Every ask, yield and refusal is a record with the
asker, the holder, the bytes and the time, read with
`openabstractions resources audit`.

The wake lease is a lease of resource `awake` (RES-A1): its rule is
`abstraction.resource/hold` on `awake`, and its lifetime stays on its holder's
connection.

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
| **instrument** | what measured: `windows-gpu-counters`, `linux-fdinfo`, or `none` |
| **held** | the sum of the verified amounts, and nothing else |
| **lease** | a grant of a resource to a program, asked back on demand |
| **attached holder** | an engine that predates OA, yielded through its own API |

[CONTRACT.md](CONTRACT.md) holds the rules: RES-T1 to RES-T5 for the table,
RES-L1 to RES-L5 for the leases and RES-A1 for the wake lease.

## Rights

Reading other programs' rows needs `abstraction.resource/table.read` on
resource `account`. A program with no rule reads its own rows, which is the
rule and not an omission: an application may always see what it holds.
`capacity`, `held` and `observed` describe the machine and name no program, so
every caller reads them.

Acquiring needs `abstraction.resource/hold` on the resource asked for. A
program with no rule is `not_permitted` before the table is read, and a
decision point that cannot answer is `unavailable`, never a permit.

Yielding an attached holder needs `abstraction.resource/yield` on
`server:<name>`, held by the service that would do the unloading. Denying it
is how a person says an engine is not to be touched.

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
