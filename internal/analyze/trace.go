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
)

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
type RawTrace struct {
	Trace      RawTraceMeta             `json:"trace"`
	TraceSteps map[string][]RawTraceRun `json:"trace_steps"`
	Config     json.RawMessage          `json:"config,omitempty"`
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
		AutoID:    raw.Trace.Domain + "." + raw.Trace.ItemID,
		Trigger:   parseTrigger(raw.Trace.Trigger),
		StartTime: raw.Trace.Timestamp.Start,
		Result:    overallResult(raw),
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

func overallResult(raw *RawTrace) StepResult {
	if raw.Trace.Error != "" || raw.Trace.Execution == "error" {
		return StepFail
	}
	return StepPass
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
		return stepOrder(paths[i]) < stepOrder(paths[j])
	})
	return paths
}

func stepOrder(path string) string {
	// Ensure trigger < condition < action ordering, then by index
	switch {
	case strings.HasPrefix(path, "trigger"):
		return "0_" + path
	case strings.HasPrefix(path, "condition"):
		return "1_" + path
	default:
		return "2_" + path
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

	resultStr := strings.ToUpper(string(ct.Result))
	fmt.Fprintf(&b, "%s  %s  %s  %s\n",
		ct.RunID, ct.AutoID, shortTimestamp(ct.StartTime), resultStr)

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
		case StepPass:
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
