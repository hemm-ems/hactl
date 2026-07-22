package analyze

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// StepResult represents the outcome of a single trace step.
type StepResult string

// Step result constants.
const (
	StepPass StepResult = "pass"
	StepFail StepResult = "fail"
	StepSkip StepResult = "skip"
	// StepUnknown means the trace did not tell us what happened — typically a
	// decode that produced nothing. It is deliberately NOT StepPass: an empty
	// decode once rendered as "  .    PASS" for every run, and both the unit
	// and the integration suites stayed green through it.
	StepUnknown StepResult = "unparsed"
)

// UnparsedMarker is the literal token FormatCondensed prints for a trace whose
// decode produced nothing. The integration harness greps every command's stdout
// for it, so it must stay in lockstep with the rendering of StepUnknown —
// TestUnparsedMarkerMatchesRendering enforces that.
const UnparsedMarker = "UNPARSED"

// StepType represents the type of a trace step.
type StepType string

// Step type constants.
const (
	StepTrigger   StepType = "trigger"
	StepCondition StepType = "cond"
	StepAction    StepType = "action"
)

// CondensedStep is one step in a condensed trace representation.
type CondensedStep struct {
	Type    StepType   `json:"type"`
	Detail  string     `json:"detail"`
	Result  StepResult `json:"result"`
	Reason  string     `json:"reason,omitempty"`
	Time    string     `json:"time,omitempty"`
	Index   int        `json:"index"`
	Skipped bool       `json:"skipped,omitempty"`
}

// CondensedTrace is the summary of a full trace.
type CondensedTrace struct {
	RunID     string          `json:"run_id"`
	AutoID    string          `json:"auto_id"`
	Trigger   string          `json:"trigger"`
	StartTime string          `json:"start_time"`
	Result    StepResult      `json:"result"`
	Steps     []CondensedStep `json:"steps"`
}

// RawTrace is the incoming trace structure from HA trace/get.
//
// The wire format is flat: HA puts the run metadata (script_execution, state,
// last_step, run_id, domain, item_id, timestamp, trigger) at the top level and
// the per-step map under the "trace" key. UnmarshalJSON maps that onto the
// Trace/TraceSteps fields the rest of this package reads.
type RawTrace struct {
	Trace      RawTraceMeta
	TraceSteps map[string][]RawTraceRun
	Config     json.RawMessage
}

// UnmarshalJSON parses HA's actual trace/get result shape: metadata at the top
// level, the step map under "trace".
//
// The previous struct tags (`json:"trace"` on the metadata, `json:"trace_steps"`
// on the steps) described a nested shape HA never emits. Against a real trace
// both fields unmarshalled empty, so Condense saw no metadata and no steps:
// every run — including failed_conditions/aborted — rendered as a bare
// "  .    PASS". The unit tests passed only because their fixtures had been
// authored in that nested shape.
func (rt *RawTrace) UnmarshalJSON(data []byte) error {
	// Metadata lives at the top level; RawTraceMeta's tags match the keys.
	if err := json.Unmarshal(data, &rt.Trace); err != nil {
		return err
	}
	// The step map lives under "trace"; config sits alongside it.
	var envelope struct {
		Steps  map[string][]RawTraceRun `json:"trace"`
		Config json.RawMessage          `json:"config,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	rt.TraceSteps = envelope.Steps
	rt.Config = envelope.Config
	return nil
}

// RawTraceMeta holds the trace-level metadata.
type RawTraceMeta struct {
	Timestamp RawTimestamp    `json:"timestamp"`
	RunID     string          `json:"run_id"`
	Domain    string          `json:"domain"`
	ItemID    string          `json:"item_id"`
	LastStep  string          `json:"last_step"`
	State     string          `json:"state"`
	Execution string          `json:"script_execution"`
	Error     string          `json:"error"`
	Trigger   json.RawMessage `json:"trigger"`
}

// RawTimestamp holds start/finish times.
type RawTimestamp struct {
	Start  string `json:"start"`
	Finish string `json:"finish"`
}

// RawTraceRun is one execution of a step in a trace.
type RawTraceRun struct {
	Path             string          `json:"path"`
	Timestamp        string          `json:"timestamp"`
	Error            string          `json:"error,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	ChangedVariables json.RawMessage `json:"changed_variables,omitempty"`
}

// Condense converts a raw HA trace into a condensed representation.
func Condense(raw *RawTrace) *CondensedTrace {
	ct := &CondensedTrace{
		RunID:     raw.Trace.RunID,
		AutoID:    autoID(raw.Trace.Domain, raw.Trace.ItemID),
		Trigger:   parseTrigger(raw.Trace.Trigger),
		StartTime: raw.Trace.Timestamp.Start,
		Result:    overallResult(raw),
	}

	// Nothing decoded at all: no identity, no steps. Whatever stray field
	// survived, we cannot claim to know how the run went.
	if degenerateDecode(raw) {
		ct.Result = StepUnknown
	}

	// Collect and sort step paths
	paths := sortedStepPaths(raw.TraceSteps)

	lastStepReached := raw.Trace.LastStep
	reachedLast := false

	for i, path := range paths {
		stepType := classifyStep(path)
		runs := raw.TraceSteps[path]

		step := CondensedStep{
			Index: i + 1,
			Type:  stepType,
		}

		if len(runs) > 0 {
			run := runs[0]
			step.Time = shortTimestamp(run.Timestamp)
			step.Detail = extractDetail(stepType, run)
			step.Result, step.Reason = stepOutcome(run)
		}

		if reachedLast {
			step.Result = StepSkip
			step.Skipped = true
		}

		ct.Steps = append(ct.Steps, step)

		if path == lastStepReached {
			reachedLast = raw.Trace.Execution == "error" || stepHasError(runs)
		}
	}

	return ct
}

// overallResult determines the run's outcome, mirroring the precedence
// internal/cmd/auto.go's traceResult uses for the same trace data: an
// explicit error always wins, then script_execution (e.g. "failed_conditions",
// "aborted", "cancelled"), falling back to the trace state when
// script_execution is empty. This keeps `trace show` and `auto ls` from
// contradicting each other about whether a run passed.
//
// When BOTH script_execution and state are empty the answer is StepUnknown,
// not StepPass. Reporting silence as success is precisely how a decode bug
// turned every run — failed, aborted, cancelled — into a green PASS.
func overallResult(raw *RawTrace) StepResult {
	if raw.Trace.Error != "" || raw.Trace.Execution == "error" {
		return StepFail
	}
	exec := raw.Trace.Execution
	if exec == "" {
		exec = raw.Trace.State
	}
	if exec == "" {
		return StepUnknown
	}
	if exec == "finished" {
		return StepPass
	}
	return StepResult(exec)
}

// degenerateDecode reports whether unmarshalling produced nothing usable: no
// domain, no item_id, no run_id and an empty step map. That is the signature of
// a wire-format mismatch rather than of any real trace, so the caller must not
// dress it up as a result.
func degenerateDecode(raw *RawTrace) bool {
	return raw.Trace.Domain == "" &&
		raw.Trace.ItemID == "" &&
		raw.Trace.RunID == "" &&
		len(raw.TraceSteps) == 0
}

// autoID joins domain and item_id, omitting the separator when either half is
// missing. An all-empty decode yields "" — never a bare ".", which used to be
// the only visible sign that nothing had parsed.
func autoID(domain, itemID string) string {
	switch {
	case domain == "":
		return itemID
	case itemID == "":
		return domain
	default:
		return domain + "." + itemID
	}
}

func classifyStep(path string) StepType {
	if strings.HasPrefix(path, "trigger") {
		return StepTrigger
	}
	if strings.HasPrefix(path, "condition") {
		return StepCondition
	}
	return StepAction
}

func sortedStepPaths(steps map[string][]RawTraceRun) []string {
	paths := make([]string, 0, len(steps))
	for p := range steps {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		return stepPathLess(paths[i], paths[j])
	})
	return paths
}

// stepPathLess orders HA step paths ("trigger", "trigger/0", "condition/0",
// "condition/0/entity_id/0", "action/0", "action/10"): first by group so
// trigger < condition < action, then segment by segment with numeric segments
// compared as numbers.
//
// The previous implementation built a single string key and let sort.Slice
// compare it lexicographically, which put "action/10" between "action/1" and
// "action/2" — every automation with ten or more actions listed its steps in
// the wrong execution order.
func stepPathLess(a, b string) bool {
	if ga, gb := stepGroup(a), stepGroup(b); ga != gb {
		return ga < gb
	}
	as := strings.Split(a, "/")
	bs := strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		if aErr == nil && bErr == nil {
			return an < bn
		}
		return as[i] < bs[i]
	}
	// One path is a prefix of the other (e.g. "trigger" vs "trigger/0", or
	// "action/0" vs "action/0/repeat/sequence/0"): the parent comes first.
	return len(as) < len(bs)
}

// stepGroup keeps the trigger < condition < action grouping. A bare "trigger"
// key — which HA emits for a service-triggered run — groups with the triggers.
func stepGroup(path string) int {
	switch {
	case strings.HasPrefix(path, "trigger"):
		return 0
	case strings.HasPrefix(path, "condition"):
		return 1
	default:
		return 2
	}
}

func extractDetail(stepType StepType, run RawTraceRun) string {
	switch stepType {
	case StepTrigger:
		return extractTriggerDetail(run)
	case StepCondition:
		return extractConditionDetail(run)
	case StepAction:
		return extractActionDetail(run)
	}
	return ""
}

// parseTrigger handles the trigger field which can be a string or an array of strings.
func parseTrigger(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return strings.Join(arr, ", ")
	}
	return string(raw)
}

func extractTriggerDetail(run RawTraceRun) string {
	if len(run.ChangedVariables) == 0 {
		return ""
	}
	var cv map[string]json.RawMessage
	if err := json.Unmarshal(run.ChangedVariables, &cv); err != nil {
		return ""
	}
	triggerData, ok := cv["trigger"]
	if !ok {
		return ""
	}
	var trigger map[string]any
	if err := json.Unmarshal(triggerData, &trigger); err != nil {
		return ""
	}
	if p, ok := trigger["platform"].(string); ok {
		return p
	}
	return ""
}

func extractConditionDetail(run RawTraceRun) string {
	// Try to extract condition type from path
	parts := strings.Split(run.Path, "/")
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}

func extractActionDetail(run RawTraceRun) string {
	if len(run.Result) == 0 {
		return ""
	}
	var result map[string]any
	if err := json.Unmarshal(run.Result, &result); err != nil {
		return ""
	}
	if params, ok := result["params"].(map[string]any); ok {
		if eid, ok := params["entity_id"].(string); ok {
			return eid
		}
	}
	return "service_call"
}

func stepOutcome(run RawTraceRun) (StepResult, string) {
	if run.Error != "" {
		return StepFail, shortenError(run.Error)
	}
	if len(run.Result) > 0 {
		var result map[string]any
		if err := json.Unmarshal(run.Result, &result); err == nil {
			if r, ok := result["result"]; ok {
				if boolVal, ok := r.(bool); ok && !boolVal {
					return StepFail, "condition_false"
				}
			}
		}
	}
	return StepPass, ""
}

func stepHasError(runs []RawTraceRun) bool {
	for _, r := range runs {
		if r.Error != "" {
			return true
		}
	}
	return false
}

func shortenError(errMsg string) string {
	// Extract the most relevant part of the error message
	if idx := strings.LastIndex(errMsg, ": "); idx >= 0 {
		msg := errMsg[idx+2:]
		if len(msg) > 40 {
			return msg[:37] + "..."
		}
		return msg
	}
	if len(errMsg) > 40 {
		return errMsg[:37] + "..."
	}
	return errMsg
}

func shortTimestamp(ts string) string {
	// Extract HH:MM:SS from ISO timestamp
	_, rest, found := strings.Cut(ts, "T")
	if !found {
		return ts
	}
	if before, _, ok := strings.Cut(rest, "."); ok {
		return before
	}
	if before, _, ok := strings.Cut(rest, "+"); ok {
		return before
	}
	return rest
}

// FormatCondensed renders a condensed trace as text.
func FormatCondensed(ct *CondensedTrace) string {
	var b strings.Builder

	// Header fields, empties dropped: a trace that decoded to nothing prints a
	// lone "UNPARSED" instead of "  .    PASS" — no punctuation standing in for
	// an automation ID we never got.
	head := make([]string, 0, 4)
	for _, f := range []string{
		ct.RunID,
		ct.AutoID,
		shortTimestamp(ct.StartTime),
		strings.ToUpper(string(ct.Result)),
	} {
		if f != "" {
			head = append(head, f)
		}
	}
	fmt.Fprintln(&b, strings.Join(head, "  "))

	for _, s := range ct.Steps {
		var marker string
		if s.Skipped {
			marker = "X"
		} else {
			marker = strconv.Itoa(s.Index)
		}

		var resultPart string
		switch s.Result {
		case StepFail:
			resultPart = "FAIL"
			if s.Reason != "" {
				resultPart += "  → " + s.Reason
			}
		case StepSkip:
			resultPart = "skipped"
		case StepPass, StepUnknown:
			resultPart = string(s.Result)
		}

		detail := s.Detail
		if detail == "" {
			detail = "-"
		}

		fmt.Fprintf(&b, " %s %-9s %-20s %s\n", marker, s.Type, detail, resultPart)
	}

	return b.String()
}
