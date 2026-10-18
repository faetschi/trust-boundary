package gate

// ProductionProfile is the gate profile of the production Pi service: the four governed tools,
// each allowed. The admission rule and the delta of a trial restrict it further; this is the
// shared base, and its Digest is part of the corpus policy pin (tbound-profile corpus-pins).
func ProductionProfile() Profile {
	return Profile{Version: ProfileVersion, Rules: []Rule{
		{Tool: "read", Effect: EffectAllow},
		{Tool: "edit", Effect: EffectAllow},
		{Tool: "write", Effect: EffectAllow},
		{Tool: "bash", Effect: EffectAllow},
	}}
}

// The labels whose SHA-256 are the session-store and the metadata policy digests of the
// production service (the policy_digest and metadata_policy_digest of a receipt and of
// run.json admission_policy). They are policy identities, not file contents.
const (
	ProductionSessionPolicyLabel  = "tbound/session-policy/v1"
	ProductionMetadataPolicyLabel = "tbound/metadata-policy/v1"
)
