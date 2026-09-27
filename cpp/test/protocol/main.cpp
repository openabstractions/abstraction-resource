// In-process loopback: a fixture Table/Leases handler behind the generated
// dispatcher, exercised through the generated templated client, the same way
// abstraction-facade/cpp/test/protocol proves its own generated codec. It also
// touches the hand-written client's pure, no-IO logic (default_endpoint and
// construction), which needs no running server.
#include <abstraction/resource/rec.h>
#include <abstraction/resource/client.hpp>
#include <iostream>
#include <stdexcept>
#include <string>

namespace wire = abstraction::resource;

// This is a dispatcher fixture, not a production C++ server.
struct FixtureTable : wire::Table {
    wire::ResourcesResult resources() override {
        wire::ResourceList result;
        result.resources = {"card:0", "memory", "awake"};
        result.observed = "2026-09-22T00:00:00.000000Z";
        wire::ResourcesResult reply;
        reply.outcome = wire::ResourceCallOutcome::Ok;
        reply.list = result;
        return reply;
    }
    wire::HoldersResult holders(const std::string& resource, const bool& fresh) override {
        wire::ResourceState result;
        result.resource = resource;
        result.capacity = 8LL << 30;
        result.held = fresh ? (2LL << 30) : (1LL << 30);
        wire::Holder holder;
        holder.program = "/usr/bin/llama-server";
        holder.account = "1000";
        holder.amount = result.held;
        holder.evidence = wire::Evidence::Verified;
        result.holders = {holder};
        result.observed = "2026-09-22T00:00:00.000000Z";
        result.instrument = "linux-fdinfo";
        wire::HoldersResult reply;
        reply.outcome = wire::ResourceCallOutcome::Ok;
        reply.state = result;
        return reply;
    }
};

struct FixtureLeases : wire::Leases {
    wire::AcquireResult acquire(const std::string& resource, const std::int64_t& amount, const std::int64_t&) override {
        wire::AcquireResult result;
        result.outcome = wire::AcquireOutcome::Acquired;
        wire::Lease lease;
        lease.id = "lease-1";
        lease.resource = resource;
        lease.amount = amount;
        lease.since = "2026-09-22T00:00:00.000000Z";
        lease.renew_by = "2026-09-22T00:05:00.000000Z";
        result.lease = lease;
        return result;
    }
    wire::ProcessBindResult bind_process(const std::string&, const std::int64_t&) override {
        wire::ProcessBindResult result;
        result.outcome = wire::ProcessBindOutcome::Unverifiable;
        return result;
    }
    wire::LeaseChangeResult renew(const std::string&) override {
        wire::LeaseChange result;
        result.applied = true;
        wire::Lease lease;
        lease.id = "lease-1";
        lease.resource = "card:0";
        lease.amount = 1;
        lease.since = "2026-09-22T00:00:00.000000Z";
        lease.renew_by = "2026-09-22T00:10:00.000000Z";
        result.lease = lease;
        wire::LeaseChangeResult reply;
        reply.outcome = wire::ResourceCallOutcome::Ok;
        reply.change = result;
        return reply;
    }
    wire::LeaseChangeResult release(const std::string&) override {
        wire::LeaseChange result;
        result.applied = true;
        wire::LeaseChangeResult reply;
        reply.outcome = wire::ResourceCallOutcome::Ok;
        reply.change = result;
        return reply;
    }
    wire::ObserveResult observe(const std::string& cursor, const std::int64_t&) override {
        wire::YieldPage result;
        result.next = cursor.empty() ? "cursor-1" : cursor;
        wire::ObserveResult reply;
        reply.outcome = wire::ResourceCallOutcome::Ok;
        reply.page = result;
        return reply;
    }
    wire::AnswerCallResult answer(const std::string&, const wire::YieldAnswer& answer, const std::string&) override {
        wire::AnswerResult result;
        result.recorded = answer == wire::YieldAnswer::Yielded || answer == wire::YieldAnswer::Refused;
        wire::AnswerCallResult reply;
        reply.outcome = wire::ResourceCallOutcome::Ok;
        reply.answer = result;
        return reply;
    }
};

static void require(bool value, const char* what) { if (!value) throw std::runtime_error(what); }

int main() {
    try {
        // The hand-written client's pure logic: no server is contacted.
        require(!abstraction::resource::client::default_endpoint().empty(), "default_endpoint is never empty");
        abstraction::resource::client::TableClient unused_table("dummy-endpoint-never-dialed");
        abstraction::resource::client::LeasesClient unused_leases("dummy-endpoint-never-dialed");
        (void)unused_table;
        (void)unused_leases;

        FixtureTable table_handler;
        wire::TableDispatcher table_transport(table_handler);
        wire::TableClient<wire::TableDispatcher> table_client(table_transport);

        auto resources = table_client.resources();
        require(resources.outcome == wire::ResourceCallOutcome::Ok && resources.list.has_value() &&
                resources.list->resources.size() == 3, "table.resources lists card:0, memory, awake");

        auto state = table_client.holders("memory", true);
        require(state.outcome == wire::ResourceCallOutcome::Ok && state.state.has_value(), "holders has an ok payload");
        require(state.state->resource == "memory", "holders echoes the resource asked for");
        require(state.state->instrument == "linux-fdinfo", "holders carries the instrument name");
        require(state.state->holders.size() == 1 && state.state->holders.front().evidence == wire::Evidence::Verified,
                "a verified holder round-trips its evidence");
        require(state.state->held == (2LL << 30), "fresh reached the fixture's fresh branch");

        FixtureLeases leases_handler;
        wire::LeasesDispatcher leases_transport(leases_handler);
        wire::LeasesClient<wire::LeasesDispatcher> leases_client(leases_transport);

        auto acquired = leases_client.acquire("card:0", 4096, 0);
        require(acquired.outcome == wire::AcquireOutcome::Acquired, "acquire reports the typed outcome");
        require(acquired.lease.has_value() && acquired.lease->amount == 4096, "an acquired lease carries its amount");

        auto renewed = leases_client.renew("lease-1");
        require(renewed.outcome == wire::ResourceCallOutcome::Ok && renewed.change.has_value() &&
                renewed.change->applied, "renew applies to the bound caller's own lease");

        auto page = leases_client.observe("", 0);
        require(page.outcome == wire::ResourceCallOutcome::Ok && page.page.has_value() &&
                page.page->next == "cursor-1", "observe advances the cursor");

        auto answered = leases_client.answer("request-1", wire::YieldAnswer::Yielded, "");
        require(answered.outcome == wire::ResourceCallOutcome::Ok && answered.answer.has_value() &&
                answered.answer->recorded, "a known yield answer is recorded");

        auto released = leases_client.release("lease-1");
        require(released.outcome == wire::ResourceCallOutcome::Ok && released.change.has_value() &&
                released.change->applied, "release ends the bound caller's own lease");

        bool typed_refusal = false;
        try {
            abstraction::resource::client::reply_data<wire::ResourceList>(
                "Resources", wire::ResourceCallOutcome::Unavailable, std::nullopt);
        } catch (const wire::ServiceError& error) {
            typed_refusal = error.code == "policy_unavailable";
        }
        require(typed_refusal, "typed unavailability remains a structured client error");

        bool missing_payload = false;
        try {
            abstraction::resource::client::reply_data<wire::ResourceList>(
                "Resources", wire::ResourceCallOutcome::Ok, std::nullopt);
        } catch (const wire::ServiceError& error) {
            missing_payload = error.code == "invalid_result";
        }
        require(missing_payload, "ok without data is an invalid result");

        std::cout << "PASS abstraction.resource table and leases round-trip through the generated dispatcher\n";
        return 0;
    } catch (const std::exception& e) {
        std::cerr << "FAIL " << e.what() << "\n";
        return 1;
    }
}
