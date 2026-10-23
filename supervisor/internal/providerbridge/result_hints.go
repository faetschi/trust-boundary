package providerbridge

import "tbound/supervisor/internal/broker/protocol"

// permittedAlternativeHints is the policy-derived, advisory model feedback table.
// It grants no authority. The adapter copy is pinned by TestAdapterHintTableEqualsGo.
var permittedAlternativeHints = map[string]string{
	"scope_protected_path":     "This path is protected. Leave it unchanged and write only inside the task's output paths.",
	"scope_path_not_covered":   "This path is outside the permitted output scope. Write only inside the task's output paths.",
	"policy_rule_deny":         "The policy does not allow this tool or action. Use another available tool on the task's files.",
	"policy_default_deny":      "The policy does not allow this tool or action. Use another available tool on the task's files.",
	"unknown_tool":             "That tool is not available. Use read, write, edit or bash.",
	"invalid_arguments":        "The tool arguments are not valid. Check the tool's argument names and types and send one corrected call.",
	"argument_digest_mismatch": "The call was not accepted because its arguments changed. Issue a new tool call with a new call id.",
	"correlation_required":     "The call was not accepted as a registered provider call. Issue a new tool call with a new call id.",
	"invalid_policy":           "The policy is unavailable, so no action is allowed. Stop and report the task as blocked.",
	"command_failed":           "The command did not succeed. Check its inputs and try a corrected command inside the workspace.",
}

// modelResultText leaves Result JSON unchanged and, for a known denial or failed
// command only, appends exactly LF + "hint: " + the fixed sentence (no final LF).
// Journaled Result bytes, their digests, and the wire schema remain unchanged.
func modelResultText(encoded []byte, result protocol.Result) string {
	reason := ""
	if result.Verdict == "DENY" {
		reason = result.ReasonCode
	} else if result.Verdict == "ALLOW" && result.Status == protocol.ResultStatusCommandFailed {
		reason = result.Status
	}
	text := string(encoded)
	if hint := permittedAlternativeHints[reason]; hint != "" {
		text += "\nhint: " + hint
	}
	return text
}
