use super::*;

struct Frames {
    reply: Vec<u8>,
}
impl FrameTransport for Frames {
    type Error = ();
    fn write_frame(&self, _: &[u8]) -> Result<(), ()> {
        Ok(())
    }
    fn exchange_frame(&self, _: &[u8]) -> Result<Vec<u8>, ()> {
        Ok(self.reply.clone())
    }
}

#[test]
fn env_endpoint_reads_the_override_only_when_set() {
    std::env::remove_var(ENV_ENDPOINT);
    assert_eq!(env_endpoint(), None);
    std::env::set_var(ENV_ENDPOINT, "explicit-endpoint");
    assert_eq!(env_endpoint(), Some("explicit-endpoint".into()));
    std::env::set_var(ENV_ENDPOINT, "");
    assert_eq!(env_endpoint(), None);
    std::env::remove_var(ENV_ENDPOINT);
}

/// CONTRACT.md: table.resources lists what the instrument and adapters can
/// report; the typed reply round-trips through the hand-written wrapper the
/// same way the Go client's wire.NewTableClient does.
#[test]
fn table_resources_decodes_the_typed_reply() {
    let reply = br#"{"version":1,"service":"abstraction.resource/table@1","method":"Resources","ok":true,"payload":{"value":{"outcome":"ok","list":{"resources":["card:0","memory","awake"],"observed":"2026-09-22T00:00:00.000000Z"}}}}"#.to_vec();
    let client = TableClient::new(Frames { reply });
    let result = client.resources().unwrap();
    assert_eq!(result.resources, vec!["card:0", "memory", "awake"]);
    assert_eq!(result.observed, "2026-09-22T00:00:00.000000Z");
}

/// CONTRACT.md RES-T4: capacity, held, observed and instrument describe the
/// machine and name no program; a verified holder carries its evidence.
#[test]
fn table_holders_decodes_a_verified_holder() {
    let reply = br#"{"version":1,"service":"abstraction.resource/table@1","method":"Holders","ok":true,"payload":{"value":{"outcome":"ok","state":{"resource":"memory","capacity":8589934592,"held":1073741824,"holders":[{"program":"/usr/bin/llama-server","account":"1000","amount":1073741824,"evidence":"verified"}],"observed":"2026-09-22T00:00:00.000000Z","instrument":"linux-fdinfo"}}}}"#.to_vec();
    let client = TableClient::new(Frames { reply });
    let result = client.holders("memory", true).unwrap();
    assert_eq!(result.resource, "memory");
    assert_eq!(result.instrument, "linux-fdinfo");
    assert_eq!(result.holders.len(), 1);
    assert_eq!(result.holders[0].evidence, Evidence::Verified);
}

/// CONTRACT.md RES-L1..RES-L3: acquire returns the typed outcome and, when
/// acquired, the lease it granted.
#[test]
fn leases_acquire_decodes_the_typed_outcome() {
    let reply = br#"{"version":1,"service":"abstraction.resource/leases@1","method":"Acquire","ok":true,"payload":{"value":{"outcome":"acquired","lease":{"id":"lease-1","resource":"card:0","amount":4096,"since":"2026-09-22T00:00:00.000000Z","renew_by":"2026-09-22T00:05:00.000000Z"},"asked":[]}}}"#.to_vec();
    let client = LeasesClient::new(Frames { reply });
    let result = client.acquire("card:0", 4096, 0).unwrap();
    assert_eq!(result.outcome, AcquireOutcome::Acquired);
    assert_eq!(result.lease.unwrap().amount, 4096);
}

/// A service refusal decodes to the typed CallError::Service, carrying the
/// resource error code the reply named.
#[test]
fn leases_answer_reports_a_service_refusal_as_a_typed_error() {
    let reply = br#"{"version":1,"service":"abstraction.resource/leases@1","method":"Answer","ok":false,"payload":{"code":"unknown_resource","message":"no such request"}}"#.to_vec();
    let client = LeasesClient::new(Frames { reply });
    match client.answer("request-1", YieldAnswer::Yielded, "") {
        Err(CallError::Service(ServiceError { code, message })) => {
            assert_eq!(code, "unknown_resource");
            assert_eq!(message, "no such request");
            assert!(RESOURCE_ERROR_CODES.contains(&code.as_str()));
        }
        other => panic!("want CallError::Service, got {other:?}"),
    }
}

#[test]
fn leases_release_decodes_an_applied_change() {
    let reply = br#"{"version":1,"service":"abstraction.resource/leases@1","method":"Release","ok":true,"payload":{"value":{"outcome":"ok","change":{"applied":true}}}}"#.to_vec();
    let client = LeasesClient::new(Frames { reply });
    let result = client.release("lease-1").unwrap();
    assert!(result.applied);
    assert!(result.lease.is_none());
}

#[test]
fn typed_refusal_and_missing_ready_payload_remain_errors() {
    let refused = br#"{"version":1,"service":"abstraction.resource/table@1","method":"Holders","ok":true,"payload":{"value":{"outcome":"unavailable"}}}"#.to_vec();
    let client = TableClient::new(Frames { reply: refused });
    match client.holders("memory", true) {
        Err(CallError::Service(ServiceError { code, .. })) => assert_eq!(code, "policy_unavailable"),
        other => panic!("want typed unavailability, got {other:?}"),
    }
    let malformed = br#"{"version":1,"service":"abstraction.resource/table@1","method":"Resources","ok":true,"payload":{"value":{"outcome":"ok"}}}"#.to_vec();
    let client = TableClient::new(Frames { reply: malformed });
    match client.resources() {
        Err(CallError::Service(ServiceError { code, .. })) => assert_eq!(code, "invalid_result"),
        other => panic!("want missing-payload refusal, got {other:?}"),
    }
}
