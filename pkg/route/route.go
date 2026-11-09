// Package route plans one model request onto one upstream target.
//
// The package is deliberately pure decision logic: it owns candidate filtering,
// mode selection, tier interpretation (compat vs strict), affinity handling and
// the candidate explanation, but it does not own health probes, bindings or
// request execution. A caller snapshots those facts into Snapshot and Plan returns
// a PlanResult. Nothing here writes shared state, so the forwarding path and
// the Monitor's routing inspector can use the same code without drifting.
//
// The planner is deterministic: for a fixed Snapshot, Request and Policy the
// chosen target and every Candidate.Score are identical across calls. Wall-clock
// state is read once through Snapshot.Now (or time.Now when it is nil), and ties
// are broken by documented stable keys rather than map iteration order.
package route

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// Mode is how a request reaches a target.
type Mode string

const (
	// ModeNative sends the client protocol to a target that serves it.
	ModeNative Mode = "native"
	// ModeLocal answers a Responses request through the local Responses runtime,
	// which executes the request as an upstream Chat Completions call.
	ModeLocal Mode = "local_responses"
	// ModeConvert translates the request to another protocol the target serves.
	ModeConvert Mode = "convert"
)

// PriorityMode controls how Target.Priority is interpreted.
type PriorityMode string

const (
	// PriorityCompat compares every candidate in the selected mode by score and
	// uses priority only to break equal-score ties. It preserves the behaviour
	// of installations where priority was a tiebreaker.
	PriorityCompat PriorityMode = "compat"
	// PriorityStrict treats priority as a tier: only the highest-priority tier
	// with a selectable candidate is considered, and lower tiers are used only
	// when every higher tier is unavailable.
	PriorityStrict PriorityMode = "strict"
)

// Target is one routable upstream. Callers fill it from their live routing
// state; route itself never probes or writes it.
//
// Healthy is explicit: a zero Target is not healthy. CooldownUntil is compared
// with Snapshot.Now (or time.Now when Now is nil); the target is cooling down
// while now is strictly before CooldownUntil.
type Target struct {
	ID            string
	ChannelID     string
	CredentialID  string
	Model         string
	WireModel     string
	Protocols     []xir.Protocol
	Priority      int
	Weight        float64
	Healthy       bool
	CooldownUntil time.Time
	Inflight      int
	LatencyMs     float64
}

// Snapshot is the immutable view of the routing world Plan works on.
//
// Bindings maps BindingKey(session, model) to a target ID. The package only
// reads the map; establishing, refreshing and expiring bindings is the
// caller's job. A binding must already be live when it is present in the map:
// the Snapshot carries no binding timestamps, so StickyTTL is not evaluated
// here (see the limitation on Policy.StickyTTL).
type Snapshot struct {
	Targets  []Target
	Bindings map[string]string
	// Now supplies the single clock reading used by one Plan call. Nil means
	// time.Now.
	Now func() time.Time
}

// BindingKey returns the stable affinity key for a session and model.
//
// The raw session value never becomes a key: it is hashed with SHA-256 together
// with the case-folded, trimmed model name, and the result is lower-case hex.
// The model is folded because model matching is case-insensitive; the session
// is only trimmed. Callers must use this helper rather than constructing the
// key themselves so the fingerprint stays consistent across processes.
func BindingKey(session, model string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(session) + "\x00" + strings.ToLower(strings.TrimSpace(model))))
	return hex.EncodeToString(sum[:])
}

// Request is the routing-relevant part of an inbound model request.
type Request struct {
	Model     string
	Protocol  xir.Protocol
	Operation xir.Operation
	Stream    bool
	Features  Features
	State     xir.StateRefs
	Session   string
}

// Features are request capabilities a target might have to support.
//
// They are carried into the plan for callers and for future capability
// filtering. Target currently has no feature or context-limit fields, so the
// planner does not filter on them yet.
type Features struct {
	Tools            bool
	Images           bool
	StructuredOutput bool
	ContextTokens    int
}

// Policy is the routing policy applied to one plan.
type Policy struct {
	// ModeOrder is the preference order for modes. An empty order means
	// native, local_responses, convert. Only the listed modes are considered;
	// duplicates and unknown values are ignored.
	ModeOrder []Mode
	// PriorityMode is compat when empty or unknown.
	PriorityMode PriorityMode
	// AllowConversion permits non-streaming translation to a target protocol
	// other than the requested one.
	AllowConversion bool
	// AllowLocal permits the local Responses runtime for a Responses request
	// whose target serves Chat Completions.
	AllowLocal bool
	// StickyTTL is retained for callers that need to expire bindings. The
	// planner cannot evaluate it: Snapshot.Bindings values are target IDs and
	// carry no timestamp. A caller must prune expired entries before building
	// the Snapshot; Plan only looks up whatever is present.
	StickyTTL time.Duration
}

// PlanResult is the selection result. Plan is also the package entry point;
// Go does not allow a function and a type to share the identifier, so the
// result type is PlanResult.
type PlanResult struct {
	Target     Target
	Mode       Mode
	Wire       xir.Protocol
	WireModel  string
	Tier       int
	Affinity   Affinity
	Candidates []Candidate
	Reason     string
}

// Candidate is one target the planner considered. Selectable reports whether
// the target passed health, cooldown, protocol/mode and affinity eligibility;
// it does not mean the target won. FilterReason is empty for a selectable
// candidate and names the first filter that rejected it otherwise.
type Candidate struct {
	Target       Target
	Mode         Mode
	Selectable   bool
	FilterReason string
	Score        float64
	Tier         int
}

// Affinity reports how a session binding affected the decision.
type Affinity struct {
	// Status is one of the AffinityStatus* values.
	Status string
	// Failed is true when a hard (server-state) affinity could not be honoured.
	Failed bool
	// KeyFingerprint is BindingKey(session, model), or empty when no session was
	// supplied. It is always a SHA-256 fingerprint, never the raw session.
	KeyFingerprint string
	// TargetID is the bound target. It is filled even when the binding was not
	// selected (soft spill or hard failure) so Explain can show the conflict.
	TargetID string
}

const (
	// AffinityStatusNone means no binding applied.
	AffinityStatusNone = "none"
	// AffinityStatusHard means server-side state required the bound target.
	// Failed reports whether that requirement could be honoured; TargetID is the
	// bound target either way.
	AffinityStatusHard = "hard"
	// AffinityStatusSoft means a chat-style binding existed. Plan.Target is the
	// bound target on a hit; when the bound target was unavailable or outside
	// the chosen tier, Plan.Target differs and TargetID still names the binding.
	AffinityStatusSoft = "soft"
)

// PlanResult.Reason values for successful and failed plans. The failure values are
// also the stable reasons carried by *PlanError.
const (
	// PlanReasonSelected is the reason of a successful plan.
	PlanReasonSelected = "selected"

	ReasonModelUnknown               = "model_unknown"
	ReasonNoCandidate                = "no_candidate"
	ReasonAllUnavailable             = "all_unavailable"
	ReasonSessionUpstreamUnavailable = "session_upstream_unavailable"
	ReasonConversionUnsupported      = "conversion_unsupported"
)

// Candidate.FilterReason values.
const (
	FilterReasonUnhealthy                      = "unhealthy"
	FilterReasonCooldown                       = "cooldown"
	FilterReasonNoProtocol                     = "no_conversion_protocol"
	FilterReasonConversionDisabled             = "conversion_disabled"
	FilterReasonStreamingConversionUnsupported = "streaming_conversion_unsupported"
	FilterReasonHardAffinityElsewhere          = "hard_affinity_bound_elsewhere"
	FilterReasonModeDisabled                   = "mode_disabled"
)

// PlanError is the typed error returned for routing failures. Reason is one of
// the stable Reason* strings. Sentinel values below make errors.Is usable.
type PlanError struct {
	Reason string
	Detail string
}

// Error implements error.
func (e *PlanError) Error() string {
	if e == nil {
		return "route: <nil>"
	}
	if e.Detail == "" {
		return "route: " + e.Reason
	}
	return "route: " + e.Reason + ": " + e.Detail
}

// Is reports whether target carries the same stable reason, so callers can use
// errors.Is(err, route.ErrNoCandidate).
func (e *PlanError) Is(target error) bool {
	other, ok := target.(*PlanError)
	return ok && e != nil && other != nil && e.Reason == other.Reason
}

// Sentinel plan errors. They carry the reason but no per-request detail; Plan
// returns a new *PlanError with the same Reason and errors.Is matches it.
var (
	ErrModelUnknown               = &PlanError{Reason: ReasonModelUnknown}
	ErrNoCandidate                = &PlanError{Reason: ReasonNoCandidate}
	ErrAllUnavailable             = &PlanError{Reason: ReasonAllUnavailable}
	ErrSessionUpstreamUnavailable = &PlanError{Reason: ReasonSessionUpstreamUnavailable}
	ErrConversionUnsupported      = &PlanError{Reason: ReasonConversionUnsupported}
)

// ReasonOf returns the stable routing reason carried by err, or "" when err is
// not a *PlanError (including context cancellation and unrelated errors).
func ReasonOf(err error) string {
	var planErr *PlanError
	if errors.As(err, &planErr) && planErr != nil {
		return planErr.Reason
	}
	return ""
}

// Plan selects one target for req.
//
// The planner follows this order:
//
//  1. Keep targets whose Model or WireModel equals req.Model, ignoring case.
//  2. Reject unhealthy and cooling-down targets.
//  3. Classify each remaining target's best mode: native when it serves the
//     request protocol; local Responses only for a Responses request with
//     AllowLocal and a target that serves Chat Completions; otherwise convert,
//     which is selectable only when AllowConversion is set and the request is
//     not streaming.
//  4. Server-side state (previous_response_id or conversation) is a hard
//     affinity: when a binding exists, only its target may be used and an
//     unavailable bound target is a session_upstream_unavailable error.
//  5. Otherwise choose the first mode in policy order with a selectable
//     candidate, then in strict mode keep the highest-priority tier in that
//     mode. A selectable bound target wins within that chosen set (soft
//     affinity); otherwise the lowest score wins, with priority and then stable
//     IDs as deterministic tiebreakers.
//
// Plan never mutates snap. On a routing failure it returns a non-nil diagnostic
// PlanResult (Candidates, Affinity and Reason populated) together with a *PlanError;
// callers that only care about success may ignore the plan. Context cancellation
// is returned unchanged with a nil plan.
func Plan(ctx context.Context, req Request, snap Snapshot, policy Policy) (*PlanResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	var now time.Time
	if snap.Now != nil {
		now = snap.Now()
	} else {
		now = time.Now()
	}

	order := normalizeModeOrder(policy.ModeOrder)
	priorityMode := normalizePriorityMode(policy.PriorityMode)
	bindingKey, boundTargetID, hasBinding := lookupBinding(req, snap)

	affinity := Affinity{Status: AffinityStatusNone, KeyFingerprint: bindingKey}
	if hasBinding {
		affinity.TargetID = boundTargetID
	}

	evaluated := evaluateTargets(req, snap.Targets, policy, now)

	if req.State.HasServerState() && hasBinding {
		return planHardAffinity(ctx, req, evaluated, order, affinity)
	}

	// Pick the first mode in policy order that has at least one selectable
	// candidate. This is what makes any native candidate beat every convert
	// candidate regardless of priority.
	chosenMode := Mode("")
	var pool []int
	for _, mode := range order {
		indices := selectableInMode(evaluated, mode)
		if len(indices) > 0 {
			chosenMode = mode
			pool = indices
			break
		}
	}

	if chosenMode == "" {
		if hasBinding {
			affinity.Status = AffinityStatusSoft
		}
		reason := noCandidateReason(evaluated)
		return failurePlan(reason, failureDetail(reason, req, affinity), evaluated, affinity)
	}

	if priorityMode == PriorityStrict {
		pool = highestTier(pool, evaluated)
	}

	// Soft affinity wins inside the chosen set. In strict mode this is already
	// the highest available tier, which keeps a recovered primary from being
	// pinned to an old backup.
	if hasBinding {
		for _, index := range pool {
			if evaluated[index].target.ID == boundTargetID {
				affinity.Status = AffinityStatusSoft
				affinity.TargetID = boundTargetID
				return selectedPlan(evaluated[index], affinity, evaluated), nil
			}
		}
		affinity.Status = AffinityStatusSoft
		affinity.TargetID = boundTargetID
	}

	best := pool[0]
	for _, index := range pool[1:] {
		if lowerScore(evaluated[index], evaluated[best]) {
			best = index
		}
	}
	return selectedPlan(evaluated[best], affinity, evaluated), nil
}

// Explain runs the same core as Plan. It exists so the inspector and the
// forwarding path cannot diverge. It does not mutate Snapshot and never writes
// bindings or inflight counters.
func Explain(ctx context.Context, req Request, snapshot Snapshot, policy Policy) (*PlanResult, error) {
	return Plan(ctx, req, snapshot, policy)
}

// planHardAffinity enforces server-side state affinity.
func planHardAffinity(ctx context.Context, req Request, evaluated []evaluatedTarget, order []Mode, affinity Affinity) (*PlanResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	boundIndex := -1
	for index := range evaluated {
		if evaluated[index].target.ID == affinity.TargetID {
			boundIndex = index
			break
		}
	}

	if boundIndex < 0 {
		filterOtherTargets(evaluated, "")
		affinity.Status = AffinityStatusHard
		affinity.Failed = true
		return failurePlan(ReasonSessionUpstreamUnavailable,
			fmt.Sprintf("session is bound to target %q, which is not present for model %q", affinity.TargetID, req.Model),
			evaluated, affinity)
	}

	bound := &evaluated[boundIndex]
	if bound.selectable && !modeAllowed(bound.mode, order) {
		bound.selectable = false
		bound.filterReason = FilterReasonModeDisabled
	}

	if !bound.selectable {
		filterOtherTargets(evaluated, bound.target.ID)
		affinity.Status = AffinityStatusHard
		affinity.Failed = true
		return failurePlan(ReasonSessionUpstreamUnavailable,
			fmt.Sprintf("session is bound to target %q, but that target cannot serve the request (%s)", bound.target.ID, bound.filterReason),
			evaluated, affinity)
	}

	// Hard affinity overrides tier and mode preference, but it does not make
	// sibling targets eligible.
	filterOtherTargets(evaluated, bound.target.ID)
	affinity.Status = AffinityStatusHard
	affinity.Failed = false
	return selectedPlan(*bound, affinity, evaluated), nil
}

// evaluateTargets classifies every model-matching target.
func evaluateTargets(req Request, targets []Target, policy Policy, now time.Time) []evaluatedTarget {
	out := make([]evaluatedTarget, 0, len(targets))
	for _, target := range targets {
		if !modelMatches(target, req.Model) {
			continue
		}
		out = append(out, evaluateTarget(req, target, policy, now))
	}
	return out
}

// evaluateTarget computes the best mode for one target and its eligibility.
func evaluateTarget(req Request, target Target, policy Policy, now time.Time) evaluatedTarget {
	result := evaluatedTarget{
		target:    target,
		score:     scoreTarget(target, now),
		wireModel: wireModelFor(target),
	}

	switch {
	case serves(target.Protocols, req.Protocol):
		result.mode = ModeNative
		result.wire = req.Protocol
	case req.Protocol == xir.ProtocolResponses && policy.AllowLocal && serves(target.Protocols, xir.ProtocolChatCompletions):
		result.mode = ModeLocal
		result.wire = xir.ProtocolChatCompletions
	default:
		result.mode = ModeConvert
		if wire, ok := conversionProtocol(req.Protocol, target.Protocols); ok {
			result.wire = wire
		}
	}

	switch {
	case !target.Healthy:
		result.filterReason = FilterReasonUnhealthy
	case onCooldown(target, now):
		result.filterReason = FilterReasonCooldown
	case result.mode == ModeConvert:
		switch {
		case result.wire == "":
			result.filterReason = FilterReasonNoProtocol
		case req.Stream:
			result.filterReason = FilterReasonStreamingConversionUnsupported
		case !policy.AllowConversion:
			result.filterReason = FilterReasonConversionDisabled
		default:
			result.selectable = true
		}
	default:
		result.selectable = true
	}
	return result
}

// evaluatedTarget is the internal form of Candidate plus the wire facts the
// public Candidate does not carry.
type evaluatedTarget struct {
	target       Target
	mode         Mode
	wire         xir.Protocol
	wireModel    string
	selectable   bool
	filterReason string
	score        float64
}

// candidate converts the internal form into the public DTO.
func (e evaluatedTarget) candidate() Candidate {
	return Candidate{
		Target:       e.target,
		Mode:         e.mode,
		Selectable:   e.selectable,
		FilterReason: e.filterReason,
		Score:        e.score,
		Tier:         e.target.Priority,
	}
}

// candidates converts every evaluated target, preserving Snapshot order.
func candidates(evaluated []evaluatedTarget) []Candidate {
	if len(evaluated) == 0 {
		return nil
	}
	out := make([]Candidate, len(evaluated))
	for index := range evaluated {
		out[index] = evaluated[index].candidate()
	}
	return out
}

// selectedPlan builds a successful plan.
func selectedPlan(selected evaluatedTarget, affinity Affinity, evaluated []evaluatedTarget) *PlanResult {
	return &PlanResult{
		Target:     selected.target,
		Mode:       selected.mode,
		Wire:       selected.wire,
		WireModel:  selected.wireModel,
		Tier:       selected.target.Priority,
		Affinity:   affinity,
		Candidates: candidates(evaluated),
		Reason:     PlanReasonSelected,
	}
}

// failurePlan builds a diagnostic plan plus the typed error.
func failurePlan(reason, detail string, evaluated []evaluatedTarget, affinity Affinity) (*PlanResult, error) {
	plan := &PlanResult{
		Affinity:   affinity,
		Candidates: candidates(evaluated),
		Reason:     reason,
	}
	if detail == "" {
		detail = reason
	}
	return plan, &PlanError{Reason: reason, Detail: detail}
}

// failureDetail renders a human-readable detail for a routing failure.
func failureDetail(reason string, req Request, affinity Affinity) string {
	switch reason {
	case ReasonModelUnknown:
		return fmt.Sprintf("no target serves model %q", req.Model)
	case ReasonAllUnavailable:
		return fmt.Sprintf("every target for model %q is unhealthy or cooling down", req.Model)
	case ReasonConversionUnsupported:
		return fmt.Sprintf("model %q can only be served by converting %s, but conversion is disabled or the request is streaming", req.Model, req.Protocol)
	case ReasonSessionUpstreamUnavailable:
		return fmt.Sprintf("session requires target %q, which is unavailable", affinity.TargetID)
	case ReasonNoCandidate:
		return fmt.Sprintf("no target can serve model %q over %s", req.Model, req.Protocol)
	default:
		return reason
	}
}

// noCandidateReason maps the evaluated set to the most specific stable reason.
func noCandidateReason(evaluated []evaluatedTarget) string {
	if len(evaluated) == 0 {
		return ReasonModelUnknown
	}

	allUnavailable := true
	conversionBlocked := false
	for _, item := range evaluated {
		switch item.filterReason {
		case FilterReasonUnhealthy, FilterReasonCooldown:
		default:
			allUnavailable = false
		}
		switch item.filterReason {
		case FilterReasonConversionDisabled, FilterReasonStreamingConversionUnsupported:
			conversionBlocked = true
		}
	}
	switch {
	case allUnavailable:
		return ReasonAllUnavailable
	case conversionBlocked:
		return ReasonConversionUnsupported
	default:
		return ReasonNoCandidate
	}
}

// selectableInMode returns the indexes of selectable candidates in mode.
func selectableInMode(evaluated []evaluatedTarget, mode Mode) []int {
	var out []int
	for index := range evaluated {
		if evaluated[index].selectable && evaluated[index].mode == mode {
			out = append(out, index)
		}
	}
	return out
}

// highestTier narrows pool to the highest Target.Priority in it.
func highestTier(pool []int, evaluated []evaluatedTarget) []int {
	if len(pool) < 2 {
		return pool
	}
	best := evaluated[pool[0]].target.Priority
	for _, index := range pool[1:] {
		if evaluated[index].target.Priority > best {
			best = evaluated[index].target.Priority
		}
	}
	out := pool[:0]
	for _, index := range pool {
		if evaluated[index].target.Priority == best {
			out = append(out, index)
		}
	}
	return out
}

// lowerScore is the deterministic selection comparator. It orders by score,
// then higher priority, then stable identity fields so a tie never depends on
// map iteration or goroutine scheduling.
func lowerScore(a, b evaluatedTarget) bool {
	if a.score != b.score {
		return a.score < b.score
	}
	if a.target.Priority != b.target.Priority {
		return a.target.Priority > b.target.Priority
	}
	if a.target.ID != b.target.ID {
		return a.target.ID < b.target.ID
	}
	if a.target.ChannelID != b.target.ChannelID {
		return a.target.ChannelID < b.target.ChannelID
	}
	if a.target.CredentialID != b.target.CredentialID {
		return a.target.CredentialID < b.target.CredentialID
	}
	if a.wireModel != b.wireModel {
		return a.wireModel < b.wireModel
	}
	return a.wire < b.wire
}

// filterOtherTargets marks every selectable target except keepID as excluded by
// hard affinity. If keepID is empty, every target is excluded.
func filterOtherTargets(evaluated []evaluatedTarget, keepID string) {
	for index := range evaluated {
		if evaluated[index].target.ID == keepID || !evaluated[index].selectable {
			continue
		}
		evaluated[index].selectable = false
		evaluated[index].filterReason = FilterReasonHardAffinityElsewhere
	}
}

// lookupBinding returns the fingerprint, bound target ID and whether a live
// binding exists.
func lookupBinding(req Request, snap Snapshot) (key, targetID string, ok bool) {
	if strings.TrimSpace(req.Session) == "" {
		return "", "", false
	}
	key = BindingKey(req.Session, req.Model)
	if snap.Bindings == nil {
		return key, "", false
	}
	targetID = strings.TrimSpace(snap.Bindings[key])
	if targetID == "" {
		return key, "", false
	}
	return key, targetID, true
}

// modelMatches reports whether target advertises the requested model by either
// its logical model name or its upstream wire name. Matching is case-insensitive
// and trims surrounding whitespace.
func modelMatches(target Target, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, candidate := range []string{target.Model, target.WireModel} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && strings.EqualFold(candidate, model) {
			return true
		}
	}
	return false
}

// wireModelFor is the name sent upstream: WireModel when set, else Model.
func wireModelFor(target Target) string {
	if target.WireModel != "" {
		return target.WireModel
	}
	return target.Model
}

// serves reports whether protocols contains protocol.
func serves(protocols []xir.Protocol, protocol xir.Protocol) bool {
	for _, candidate := range protocols {
		if candidate == protocol {
			return true
		}
	}
	return false
}

// conversionProtocol picks the wire protocol for a conversion. The preference
// is fixed (chat, responses, messages, gemini) after excluding the requested
// protocol, so a target serving several alternatives always converts the same
// way.
func conversionProtocol(request xir.Protocol, protocols []xir.Protocol) (xir.Protocol, bool) {
	for _, candidate := range []xir.Protocol{
		xir.ProtocolChatCompletions,
		xir.ProtocolResponses,
		xir.ProtocolMessages,
		xir.ProtocolGemini,
	} {
		if candidate == request {
			continue
		}
		if serves(protocols, candidate) {
			return candidate, true
		}
	}
	return "", false
}

// normalizeModeOrder validates and deduplicates the policy order. An empty or
// entirely invalid order falls back to native, local_responses, convert.
func normalizeModeOrder(order []Mode) []Mode {
	if len(order) == 0 {
		return []Mode{ModeNative, ModeLocal, ModeConvert}
	}
	out := make([]Mode, 0, len(order))
	seen := make(map[Mode]bool, len(order))
	for _, mode := range order {
		switch mode {
		case ModeNative, ModeLocal, ModeConvert:
		default:
			continue
		}
		if seen[mode] {
			continue
		}
		seen[mode] = true
		out = append(out, mode)
	}
	if len(out) == 0 {
		return []Mode{ModeNative, ModeLocal, ModeConvert}
	}
	return out
}

// normalizePriorityMode maps an empty or unknown value to compat.
func normalizePriorityMode(mode PriorityMode) PriorityMode {
	if mode == PriorityStrict {
		return PriorityStrict
	}
	return PriorityCompat
}

// modeAllowed reports whether mode is part of the effective order.
func modeAllowed(mode Mode, order []Mode) bool {
	for _, candidate := range order {
		if candidate == mode {
			return true
		}
	}
	return false
}

// onCooldown reports whether target is cooling down at now.
func onCooldown(target Target, now time.Time) bool {
	return !target.CooldownUntil.IsZero() && now.Before(target.CooldownUntil)
}
