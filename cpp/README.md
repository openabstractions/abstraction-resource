# C++ resource table and leases

Install the runtime first: https://openabstractions.org/adopt.html

`abstraction::resource_client` is supplied by the installed `abstraction_resource`
CMake package. It uses generated Table and Leases codecs and shared identity IPC.

`resource::client::TableClient(endpoint).resources()` lists the resources this
machine's instrument and adapters can report. `holders(resource, fresh)` reads
who holds `resource` now; `fresh` asks the service to read its instrument again
rather than stand on the sample it holds (CONTRACT.md RES-T1..RES-T5).

`resource::client::LeasesClient(endpoint).acquire(resource, amount, wait_ms)`
asks for `amount` bytes of `resource`, waiting up to `wait_ms` for holders to
yield, and returns the typed outcome plus every holder the service asked
(RES-L1..RES-L3). `renew` and `release` act on the bound caller's own lease.
`observe` long-polls yield requests addressed to the bound caller; `answer`
records the bound caller's answer, confirmed against the instrument before the
asker is told (RES-L3).

Both clients default to `ABSTRACTION_RESOURCE_ENDPOINT`, or the one endpoint the
Go client names `resource-table-v1`: table and leases are the same running
service, distinguished by the frame's service name rather than the endpoint.
`TableClient(endpoint)` and `LeasesClient(endpoint)` use a fresh five-second
budget per call. The constructor accepting `ipc::Deadline` retains a caller
budget, and `with_cancellation(token)`/`with_server_expectation(server)` retain
shared cancellation and server identity, matching `abstraction::rights::Client`.

For resolution use optional `abstraction_facade_resource`, target
`abstraction::facade_resource`, header `abstraction/facade/resource.hpp`.
`resolve_resource_table`/`resolve_resource_leases` bind these two clients
through the shared resolver.

**Not yet published.** The `abstraction-resource` repository does not exist
yet; this layer publishes there, first tag `go/v0.1.0`, once the owner
creates it, after the published `identity` and `rights` modules this layer
depends on (`scripts/check.baseline`).
