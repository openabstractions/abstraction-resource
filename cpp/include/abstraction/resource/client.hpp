#pragma once
#include <abstraction/resource/rec.h>
#include <abstraction/ipc/frame.hpp>
#include <cstdlib>
#include <optional>
#include <string>
#include <utility>

namespace abstraction::resource::client {

// ABSTRACTION_RESOURCE_ENDPOINT overrides discovery. Otherwise table and leases
// are the same running service, on the one endpoint the Go client names
// resource-table-v1 (go/client/client.go DefaultEndpoint); the frame's service
// field, not the endpoint, selects Table or Leases.
inline std::string default_endpoint() {
    if (const char* value = std::getenv("ABSTRACTION_RESOURCE_ENDPOINT")) { if (*value) return value; }
#ifdef _WIN32
    return R"(\\.\pipe\openabstractions-resource-table-v1)";
#else
    if (const char* value = std::getenv("XDG_RUNTIME_DIR")) { if (*value) return std::string(value) + "/openabstractions-resource-table-v1.sock"; }
    const char* temp = std::getenv("TMPDIR");
    const char* user = std::getenv("USER");
    return std::string(temp && *temp ? temp : "/tmp") + "/openabstractions-resource-table-v1-" + (user ? user : "") + ".sock";
#endif
}

// The compact client returns data and raises a structured service error for
// typed refusals. Only ok carries data.
template <typename T>
T reply_data(std::string_view operation, ResourceCallOutcome outcome, std::optional<T> data) {
    if (outcome == ResourceCallOutcome::Ok && data) return std::move(*data);
    if (outcome == ResourceCallOutcome::Ok || data || wire_name(outcome).empty())
        throw ServiceError("invalid_result", std::string(operation) + " outcome/payload mismatch");
    std::string code(wire_name(outcome));
    if (outcome == ResourceCallOutcome::Unavailable) code = "policy_unavailable";
    if (outcome == ResourceCallOutcome::Invalid) code = "invalid_request";
    throw ServiceError(code, std::string(operation) + " outcome " + std::string(wire_name(outcome)));
}

// Read-only: who holds how much of a scarce resource on this machine, verified
// by the instrument or claimed by the holder (CONTRACT.md RES-T1..RES-T5). It
// measures nothing itself.
class TableClient {
public:
    explicit TableClient(std::string endpoint = default_endpoint()) : transport_(std::move(endpoint), 5000, 1u << 20) {}
    // Explicit operation scope; copies retain the same absolute deadline.
    TableClient(std::string endpoint, ipc::Deadline deadline) : transport_(std::move(endpoint), deadline, 1u << 20) {}
    TableClient with_server_expectation(std::optional<ipc::ServerExpectation> server) const {
        auto copy = *this; copy.transport_ = transport_.with_server_expectation(std::move(server)); return copy;
    }
    TableClient with_cancellation(ipc::CancellationToken token) const {
        auto copy = *this; copy.transport_ = transport_.with_cancellation(std::move(token)); return copy;
    }

    // The resources this machine's instrument and adapters can report.
    ResourceList resources() const {
        auto transport = transport_;
        ::abstraction::resource::TableClient<ipc::FrameTransport> raw(transport);
        auto reply = raw.resources();
        return reply_data("Resources", reply.outcome, std::move(reply.list));
    }
    // Who holds resource now. fresh reads the instrument again; otherwise the
    // last sample within its age bound (RES-T2). Gated by
    // abstraction.resource/table.read on resource account; a program always
    // sees its own rows, and capacity/held/observed/instrument describe the
    // machine to every caller (RES-T4).
    ResourceState holders(const std::string& resource, bool fresh = false) const {
        auto transport = transport_;
        ::abstraction::resource::TableClient<ipc::FrameTransport> raw(transport);
        auto reply = raw.holders(resource, fresh);
        return reply_data("Holders", reply.outcome, std::move(reply.state));
    }
private:
    ipc::FrameTransport transport_;
};

// Leases on a scarce resource, asked back on demand (CONTRACT.md RES-L1..RES-L5,
// RES-A1). A holder that predates OA is an attached holder: the service yields
// on its behalf through the host's own mechanism. Version 1 kills nothing.
class LeasesClient {
public:
    explicit LeasesClient(std::string endpoint = default_endpoint()) : transport_(std::move(endpoint), 5000, 1u << 20) {}
    LeasesClient(std::string endpoint, ipc::Deadline deadline) : transport_(std::move(endpoint), deadline, 1u << 20) {}
    LeasesClient with_server_expectation(std::optional<ipc::ServerExpectation> server) const {
        auto copy = *this; copy.transport_ = transport_.with_server_expectation(std::move(server)); return copy;
    }
    LeasesClient with_cancellation(ipc::CancellationToken token) const {
        auto copy = *this; copy.transport_ = transport_.with_cancellation(std::move(token)); return copy;
    }

    // Asks for amount bytes of resource for the bound caller, waiting up to
    // wait_ms (0..120000) for holders to yield. Returns the typed outcome and
    // every holder the service asked, whatever the outcome (RES-L1..RES-L3).
    AcquireResult acquire(const std::string& resource, std::int64_t amount, std::int64_t wait_ms) const {
        auto transport = transport_;
        ::abstraction::resource::LeasesClient<ipc::FrameTransport> raw(transport);
        return raw.acquire(resource, amount, wait_ms);
    }
    // Extends renew_by of the bound caller's own lease.
    LeaseChange renew(const std::string& lease) const {
        auto transport = transport_;
        ::abstraction::resource::LeasesClient<ipc::FrameTransport> raw(transport);
        auto reply = raw.renew(lease);
        return reply_data("Renew", reply.outcome, std::move(reply.change));
    }
    // Ends the bound caller's own lease now.
    LeaseChange release(const std::string& lease) const {
        auto transport = transport_;
        ::abstraction::resource::LeasesClient<ipc::FrameTransport> raw(transport);
        auto reply = raw.release(lease);
        return reply_data("Release", reply.outcome, std::move(reply.change));
    }
    // Yield requests addressed to the bound caller since cursor, waiting up to
    // wait_ms (0..30000) for one.
    YieldPage observe(const std::string& cursor, std::int64_t wait_ms) const {
        auto transport = transport_;
        ::abstraction::resource::LeasesClient<ipc::FrameTransport> raw(transport);
        auto reply = raw.observe(cursor, wait_ms);
        return reply_data("Observe", reply.outcome, std::move(reply.page));
    }
    // Records the bound caller's answer to one yield request. yielded is
    // confirmed against the instrument before the asker is told (RES-L3).
    AnswerResult answer(const std::string& request, YieldAnswer answer_value, const std::string& reason) const {
        auto transport = transport_;
        ::abstraction::resource::LeasesClient<ipc::FrameTransport> raw(transport);
        auto reply = raw.answer(request, answer_value, reason);
        return reply_data("Answer", reply.outcome, std::move(reply.answer));
    }
private:
    ipc::FrameTransport transport_;
};

}
