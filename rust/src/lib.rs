//! Hand-written typed client over the generated abstraction.resource records,
//! following the Go client package (go/client): a thin wrapper that reads
//! table@1 and calls leases@1 through the shared framed transport. It
//! measures nothing itself.
pub use abstraction_frame::FrameTransport;
#[path = "../../rs/abstraction/resource/rec.rs"]
pub mod wire;

pub use wire::{
    service_error_code, AcquireOutcome, AcquireResult, AnswerResult, CallError, Evidence, Holder,
    Lease, LeaseChange, Refusal, ResourceCallOutcome, ResourceList, ResourceState, ServiceError, YieldAnswer,
    YieldPage, YieldRecord, YieldRequest, KNOWN_RESOURCES, RESOURCE_ERROR_CODES,
};

/// Names the endpoint for a caller that resolves nothing. Table and leases are
/// the one running service, on the endpoint the Go client names
/// resource-table-v1 (go/client/client.go DefaultEndpoint); the frame's
/// service field, not the endpoint, selects the profile.
pub const ENV_ENDPOINT: &str = "ABSTRACTION_RESOURCE_ENDPOINT";

/// The explicit endpoint override, when set and non-empty. Discovering the
/// installed runtime's endpoint when no override is set is left to the
/// transport the application supplies, as in the Go client's DefaultEndpoint.
pub fn env_endpoint() -> Option<String> {
    std::env::var(ENV_ENDPOINT).ok().filter(|s| !s.is_empty())
}

// Keep the compact client data API while retaining typed refusal codes from
// service reply envelopes. The payload exists exactly for ok.
fn reply_data<T, E>(operation: &'static str, outcome: ResourceCallOutcome, data: Option<T>) -> Result<T, CallError<E>> {
    match (outcome, data) {
        (ResourceCallOutcome::Ok, Some(value)) => Ok(value),
        (ResourceCallOutcome::Ok, None) | (_, Some(_)) => Err(CallError::Service(ServiceError {
            code: "invalid_result".into(),
            message: format!("{operation} outcome/payload mismatch"),
        })),
        (outcome, None) => {
            let code = match outcome {
                ResourceCallOutcome::Unavailable => "policy_unavailable",
                ResourceCallOutcome::Invalid => "invalid_request",
                _ => outcome.as_str(),
            };
            Err(CallError::Service(ServiceError {
                code: code.into(),
                message: format!("{operation} outcome {}", outcome.as_str()),
            }))
        }
    }
}

/// Read-only: who holds how much of a scarce resource on this machine,
/// verified by the instrument or claimed by the holder (CONTRACT.md
/// RES-T1..RES-T5). It measures nothing itself.
pub struct TableClient<T> {
    transport: T,
}

impl<T> TableClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }
    pub fn transport(&self) -> &T {
        &self.transport
    }
}

impl<T: FrameTransport> TableClient<T> {
    /// The resources this machine's instrument and adapters can report.
    pub fn resources(&self) -> Result<ResourceList, CallError<T::Error>> {
        use wire::Table;
        let reply = wire::TableClient::new(&self.transport).resources()?;
        reply_data("Resources", reply.outcome, reply.list)
    }
    /// Who holds resource now. fresh reads the instrument again; otherwise the
    /// last sample within its age bound (RES-T2). Gated by
    /// abstraction.resource/table.read on resource account; a program always
    /// sees its own rows, and capacity/held/observed/instrument describe the
    /// machine to every caller (RES-T4).
    pub fn holders(
        &self,
        resource: impl Into<String>,
        fresh: bool,
    ) -> Result<ResourceState, CallError<T::Error>> {
        use wire::Table;
        let reply = wire::TableClient::new(&self.transport).holders(resource.into(), fresh)?;
        reply_data("Holders", reply.outcome, reply.state)
    }
}

/// Leases on a scarce resource, asked back on demand (CONTRACT.md
/// RES-L1..RES-L5, RES-A1). A holder that predates OA is an attached holder:
/// the service yields on its behalf through the host's own mechanism. Version
/// 1 kills nothing and boosts nothing.
pub struct LeasesClient<T> {
    transport: T,
}

impl<T> LeasesClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }
    pub fn transport(&self) -> &T {
        &self.transport
    }
}

impl<T: FrameTransport> LeasesClient<T> {
    /// Asks for amount bytes of resource for the bound caller, waiting up to
    /// wait_ms (0..120000) for holders to yield. Returns the typed outcome and
    /// every holder the service asked, whatever the outcome (RES-L1..RES-L3).
    pub fn acquire(
        &self,
        resource: impl Into<String>,
        amount: i64,
        wait_ms: i64,
    ) -> Result<AcquireResult, CallError<T::Error>> {
        use wire::Leases;
        wire::LeasesClient::new(&self.transport).acquire(resource.into(), amount, wait_ms)
    }
    /// Extends renew_by of the bound caller's own lease.
    pub fn renew(&self, lease: impl Into<String>) -> Result<LeaseChange, CallError<T::Error>> {
        use wire::Leases;
        let reply = wire::LeasesClient::new(&self.transport).renew(lease.into())?;
        reply_data("Renew", reply.outcome, reply.change)
    }
    /// Ends the bound caller's own lease now.
    pub fn release(&self, lease: impl Into<String>) -> Result<LeaseChange, CallError<T::Error>> {
        use wire::Leases;
        let reply = wire::LeasesClient::new(&self.transport).release(lease.into())?;
        reply_data("Release", reply.outcome, reply.change)
    }
    /// Yield requests addressed to the bound caller since cursor, waiting up
    /// to wait_ms (0..30000) for one.
    pub fn observe(
        &self,
        cursor: impl Into<String>,
        wait_ms: i64,
    ) -> Result<YieldPage, CallError<T::Error>> {
        use wire::Leases;
        let reply = wire::LeasesClient::new(&self.transport).observe(cursor.into(), wait_ms)?;
        reply_data("Observe", reply.outcome, reply.page)
    }
    /// Records the bound caller's answer to one yield request. yielded is
    /// confirmed against the instrument before the asker is told (RES-L3).
    pub fn answer(
        &self,
        request: impl Into<String>,
        answer: YieldAnswer,
        reason: impl Into<String>,
    ) -> Result<AnswerResult, CallError<T::Error>> {
        use wire::Leases;
        let reply = wire::LeasesClient::new(&self.transport).answer(request.into(), answer, reason.into())?;
        reply_data("Answer", reply.outcome, reply.answer)
    }
}

#[cfg(test)]
mod tests;
