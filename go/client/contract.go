package client

// The generated contract types this package's API reaches, re-exported so an
// application names them through this package and never imports the generated
// one. scripts/idiom_check.py refuses a reachable type this file leaves out.

import (
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

type Holder = wire.Holder

type ResourceList = wire.ResourceList

type ResourceState = wire.ResourceState

type ServiceError = wire.ServiceError

type Lease = wire.Lease

type LeaseChange = wire.LeaseChange

type ProcessBindResult = wire.ProcessBindResult

type ProcessBindOutcome = wire.ProcessBindOutcome

const (
	ProcessBindOutcomeBound        = wire.ProcessBindOutcomeBound
	ProcessBindOutcomeUnverifiable = wire.ProcessBindOutcomeUnverifiable
	ProcessBindOutcomeRefused      = wire.ProcessBindOutcomeRefused
	ProcessBindOutcomeInvalid      = wire.ProcessBindOutcomeInvalid
	ProcessBindOutcomeForbidden    = wire.ProcessBindOutcomeForbidden
	ProcessBindOutcomeUnavailable  = wire.ProcessBindOutcomeUnavailable
)

func ProcessBindOutcomeValues() []ProcessBindOutcome { return wire.ProcessBindOutcomeValues() }

func ParseProcessBindOutcome(word string) (ProcessBindOutcome, bool) {
	return wire.ParseProcessBindOutcome(word)
}

type AcquireResult = wire.AcquireResult

type YieldRecord = wire.YieldRecord

type YieldRequest = wire.YieldRequest

type YieldPage = wire.YieldPage

type AnswerResult = wire.AnswerResult

type AcquireOutcome = wire.AcquireOutcome

const (
	AcquireOutcomeAcquired       = wire.AcquireOutcomeAcquired
	AcquireOutcomeInsufficient   = wire.AcquireOutcomeInsufficient
	AcquireOutcomeHoldersRefused = wire.AcquireOutcomeHoldersRefused
	AcquireOutcomeNotPermitted   = wire.AcquireOutcomeNotPermitted
	AcquireOutcomeUnavailable    = wire.AcquireOutcomeUnavailable
	AcquireOutcomeInvalid        = wire.AcquireOutcomeInvalid
	AcquireOutcomeForbidden      = wire.AcquireOutcomeForbidden
)

// AcquireOutcomeValues returns every member of AcquireOutcome in declaration order, in a new slice.
func AcquireOutcomeValues() []AcquireOutcome { return wire.AcquireOutcomeValues() }

// ParseAcquireOutcome returns the member named by an exact wire word.
func ParseAcquireOutcome(word string) (AcquireOutcome, bool) { return wire.ParseAcquireOutcome(word) }

type YieldAnswer = wire.YieldAnswer

const (
	YieldAnswerYielded = wire.YieldAnswerYielded
	YieldAnswerRefused = wire.YieldAnswerRefused
)

// YieldAnswerValues returns every member of YieldAnswer in declaration order, in a new slice.
func YieldAnswerValues() []YieldAnswer { return wire.YieldAnswerValues() }

// ParseYieldAnswer returns the member named by an exact wire word.
func ParseYieldAnswer(word string) (YieldAnswer, bool) { return wire.ParseYieldAnswer(word) }

type Evidence = wire.Evidence

const (
	EvidenceVerified = wire.EvidenceVerified
	EvidenceClaimed  = wire.EvidenceClaimed
)

// EvidenceValues returns every member of Evidence in declaration order, in a new slice.
func EvidenceValues() []Evidence { return wire.EvidenceValues() }

// ParseEvidence returns the member named by an exact wire word.
func ParseEvidence(word string) (Evidence, bool) { return wire.ParseEvidence(word) }

type ServiceErrorCode = wire.ServiceErrorCode

const (
	ServiceErrorCodeHandlerError      = wire.ServiceErrorCodeHandlerError
	ServiceErrorCodeInvalidResult     = wire.ServiceErrorCodeInvalidResult
	ServiceErrorCodeUnknownVersion    = wire.ServiceErrorCodeUnknownVersion
	ServiceErrorCodeUnknownService    = wire.ServiceErrorCodeUnknownService
	ServiceErrorCodeUnknownMethod     = wire.ServiceErrorCodeUnknownMethod
	ServiceErrorCodeWrongMode         = wire.ServiceErrorCodeWrongMode
	ServiceErrorCodeInternal          = wire.ServiceErrorCodeInternal
	ServiceErrorCodeInvalidRequest    = wire.ServiceErrorCodeInvalidRequest
	ServiceErrorCodeCallerRefused     = wire.ServiceErrorCodeCallerRefused
	ServiceErrorCodeUnknownResource   = wire.ServiceErrorCodeUnknownResource
	ServiceErrorCodePolicyUnavailable = wire.ServiceErrorCodePolicyUnavailable
	ServiceErrorCodeForbidden         = wire.ServiceErrorCodeForbidden
)

// ServiceErrorCodeValues returns every member of ServiceErrorCode in declaration order, in a new slice.
func ServiceErrorCodeValues() []ServiceErrorCode { return wire.ServiceErrorCodeValues() }

// KnownResources are the resource names the contract names today: card:<n>
// for an accelerator's memory, memory for system memory, awake for the wake
// hold. On an APU card:<n> and memory report the same bytes; a reader never
// sums them.
var KnownResources = wire.KnownResources
