// Go-owned table: supervisor/internal/providerbridge/result_hints.go.
// Golden-file checked by TestAdapterHintTableEqualsGo; edit the Go table first.
export const permittedAlternativeHints: Readonly<Record<string, string>> = {
  "argument_digest_mismatch": "The call was not accepted because its arguments changed. Issue a new tool call with a new call id.",
  "command_failed": "The command did not succeed. Check its inputs and try a corrected command inside the workspace.",
  "correlation_required": "The call was not accepted as a registered provider call. Issue a new tool call with a new call id.",
  "invalid_arguments": "The tool arguments are not valid. Check the tool's argument names and types and send one corrected call.",
  "invalid_policy": "The policy is unavailable, so no action is allowed. Stop and report the task as blocked.",
  "policy_default_deny": "The policy does not allow this tool or action. Use another available tool on the task's files.",
  "policy_rule_deny": "The policy does not allow this tool or action. Use another available tool on the task's files.",
  "scope_path_not_covered": "This path is outside the permitted output scope. Write only inside the task's output paths.",
  "scope_protected_path": "This path is protected. Leave it unchanged and write only inside the task's output paths.",
  "unknown_tool": "That tool is not available. Use read, write, edit or bash."
};
