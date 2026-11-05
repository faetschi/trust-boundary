//go:build linux

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/sessionrepo"
)

// Empty command journals need no active image/profile. Nonempty journals select
// immutable signed historical bindings; today's registration is never replay authority.
func openProductD09CommandHistory(journal *audit.Journal, link productsession.TurnLink) (governedRecoveryCommandVerifier, error) {
	return openProductD09CommandHistoryAt("/", journal, link)
}

func openProductD09CommandHistoryAt(root string, journal *audit.Journal, link productsession.TurnLink) (governedRecoveryCommandVerifier, error) {
	if journal == nil {
		return nil, errors.New("missing product command journal")
	}
	trace, err := journal.Trace()
	if err != nil {
		return nil, err
	}
	if len(trace.Records) == 0 {
		return nil, nil
	}
	profileDigest := ""
	for _, record := range trace.Records {
		var header struct {
			SchemaVersion string `json:"schema_version"`
			ProfileDigest string `json:"profile_digest"`
		}
		if record.Event.Kind != productionD09SettledKind || protocol.ValidateStrictJSON(record.Event.Data) != nil || json.Unmarshal(record.Event.Data, &header) != nil ||
			(header.SchemaVersion != productionD09SettledSchemaSuccess && header.SchemaVersion != productionD09SettledSchemaDenied) || !validDigestText(header.ProfileDigest) {
			return nil, errors.New("unsupported product command history record or profile digest")
		}
		if profileDigest != "" && profileDigest != header.ProfileDigest {
			return nil, errors.New("product command history mixes signed profiles")
		}
		profileDigest = header.ProfileDigest
	}
	base := filepath.Join(root, productsetup.PodmanConfigDir, "hosts", strings.TrimPrefix(profileDigest, "sha256:"))
	read := func(name string, limit int64) ([]byte, error) {
		return productImageRead(root, filepath.Join(base, name), limit)
	}
	decode := func(raw []byte, err error) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	}
	key, err := decode(productImageRead(root, filepath.Join(root, productsetup.RegistrationPublicKeyPath), 1024))
	if err != nil {
		return nil, err
	}
	historicalKey, err := decode(read("registration.pub", 1024))
	if err != nil || string(historicalKey) != string(key) {
		return nil, errors.Join(errors.New("historical product signing-key rotation is refused"), err)
	}
	registrationRaw, err := read("registration.json", int64(productsession.MaxRegistrationBytes))
	if err != nil {
		return nil, err
	}
	registrationSig, err := decode(read("registration.json.sig", 1024))
	if err != nil {
		return nil, err
	}
	registration, err := productsession.VerifyRegistration(registrationRaw, registrationSig, ed25519.PublicKey(key))
	if err != nil {
		return nil, err
	}
	profileRaw, err := read("host-profile.json", 1<<20)
	if err != nil {
		return nil, err
	}
	profileSig, err := decode(read("host-profile.json.sig", 1024))
	if err != nil {
		return nil, err
	}
	profile, err := piruntime.VerifyLinuxSignedHostProfile(profileRaw, profileSig, ed25519.PublicKey(key))
	if err != nil {
		return nil, err
	}
	templateRaw, err := read("d09-cell.json", 1<<20)
	if err != nil {
		return nil, err
	}
	template, err := piruntime.DecodeProductD09CellTemplate(templateRaw)
	if err != nil {
		return nil, err
	}
	templateDigest, err := template.Digest()
	if err != nil || profile.Digest != profileDigest || len(profile.PlaceholderFields()) != 0 || profile.Mode != piruntime.ModeProduction ||
		profile.ContainmentProfile != piruntime.D09ContainmentProfile || profile.SettlementProfile != piruntime.D09SettlementProfile ||
		profile.ContainmentProfileDigest != templateDigest || template.ProductRegistrationDigest != registration.Digest || template.ExpectedImageDigest != registration.ImageDigest ||
		profile.OfflineVerifierDigest != piruntime.OfflineVerifierDigest(template.CosignDigest, template.CosignPublicKeyDigest) ||
		template.CosignPublicKeyPath != productsetup.CommandCosignPublicKeyPath || template.SeccompProfilePath != productsetup.CommandSeccompPath || template.OCILayoutPath != productsetup.CommandOCILayoutPath {
		return nil, errors.Join(errors.New("historical product profile, template, registration or image differs"), err)
	}
	for _, artifact := range []struct{ name, digest string }{{"seccomp.json", template.SeccompProfileDigest}, {"cosign.pub", template.CosignPublicKeyDigest}} {
		raw, err := read(artifact.name, 1<<20)
		if err != nil || piruntime.DigestBytes(raw) != artifact.digest {
			return nil, errors.Join(errors.New("historical product public artifact differs"), err)
		}
	}
	for _, record := range trace.Records {
		var event struct {
			Receipt struct {
				ImageDigest string `json:"image_digest"`
			} `json:"receipt"`
		}
		if json.Unmarshal(record.Event.Data, &event) != nil || event.Receipt.ImageDigest != registration.ImageDigest {
			return nil, errors.New("historical command receipt uses a different registered image")
		}
	}
	return productD09CommandHistory(profile, template)(journal, link)
}

// productTurnPaths names the retained evidence of one finished product turn. CommandJournalPath and
// Link are empty for evidence that has no command journal.
type productTurnPaths struct {
	StoreRoot           string
	StoreJournalPath    string
	ProviderJournalPath string
	CommandJournalPath  string
	Link                productsession.TurnLink
}

// retainedProductTurn holds a finished turn's Store and audit journals, opened without repair, for
// ReplayGovernedPredecessorContext in a fresh process. CommandJournal and Commands are nil for
// evidence without a command journal, and Commands is nil without a history opener.
type retainedProductTurn struct {
	Store           *sessionrepo.Store
	StoreJournal    *audit.Journal
	ProviderJournal *audit.Journal
	CommandJournal  *audit.Journal
	Commands        governedRecoveryCommandVerifier
	closers         []func() error
}

// openRetainedProductTurn opens existing evidence only: it creates no journal or directory, and every
// Store operation and transition is checked against its durable authorization in the retained Store
// journal, never against a live authority. options carries the turn's Store policy, limits, xattr
// profile and admission scope; its journal is replaced and its callbacks are unused. A retained Bash
// effect is verified against the command settlements the turn retained, through openHistory; without
// one the historical verifier refuses the Store.
func openRetainedProductTurn(paths productTurnPaths, options sessionrepo.Options,
	openHistory productCommandHistoryOpener) (*retainedProductTurn, error) {
	turn := &retainedProductTurn{}
	fail := func(err error) (*retainedProductTurn, error) { return nil, errors.Join(err, turn.Close()) }
	providerJournal, err := openExistingRecoveryJournal(paths.ProviderJournalPath)
	if err != nil {
		return fail(fmt.Errorf("open retained provider journal: %w", err))
	}
	turn.closers = append(turn.closers, providerJournal.Close)
	storeJournal, err := openExistingRecoveryJournal(paths.StoreJournalPath)
	if err != nil {
		return fail(fmt.Errorf("open retained Store journal: %w", err))
	}
	turn.closers = append(turn.closers, storeJournal.Close)
	if paths.CommandJournalPath != "" {
		if turn.CommandJournal, err = openExistingRecoveryJournal(paths.CommandJournalPath); err != nil {
			return fail(fmt.Errorf("open retained command journal: %w", err))
		}
		turn.closers = append(turn.closers, turn.CommandJournal.Close)
		if openHistory != nil {
			if turn.Commands, err = openHistory(turn.CommandJournal, paths.Link); err != nil {
				return fail(fmt.Errorf("open retained command settlements: %w", err))
			}
		}
	}
	root, err := openExistingPrivateRecoveryDirectory(paths.StoreRoot)
	if err != nil {
		return fail(fmt.Errorf("open retained Store root: %w", err))
	}
	var settlement historicalSettlementVerifier
	if turn.Commands != nil {
		settlement = turn.Commands.VerifySettlement
	}
	historical, err := newGovernedHistoricalStoreVerifier(storeJournal, options.PolicyDigest, options.MetadataPolicyDigest, settlement)
	if err != nil {
		_ = root.Close()
		return fail(fmt.Errorf("index retained Store authorizations: %w", err))
	}
	for _, effect := range historical.initialTrace.Effects {
		var intent governedReplayOperationIntent
		if effect.Unresolved && decodeCanonicalGoverned(effect.Intent, &intent) == nil && intent.Request.Tool == "bash" {
			_ = root.Close()
			return fail(errHistoricalBashContext)
		}
	}
	options.Journal, options.ExposureBinder = storeJournal, nil
	// The preview repairs nothing and checks every effect against its retained authorization record.
	store, err := sessionrepo.OpenRecoveryPreview(root, options)
	if closeErr := root.Close(); err == nil && closeErr != nil {
		_ = store.Close()
		err = closeErr
	}
	if err != nil {
		return fail(fmt.Errorf("open retained Store: %w", err))
	}
	turn.closers = append(turn.closers, store.Close)
	bundle, err := store.EvidenceBundle()
	if err == nil {
		err = historical.VerifyBundle(bundle)
	}
	if err == nil && turn.Commands != nil {
		err = turn.Commands.VerifyStoreBundle(bundle)
	}
	if err != nil {
		return fail(fmt.Errorf("verify retained Store authorizations: %w", err))
	}
	turn.Store, turn.StoreJournal, turn.ProviderJournal = store, storeJournal, providerJournal
	return turn, nil
}

// Close closes everything openRetainedProductTurn opened, newest first.
func (t *retainedProductTurn) Close() error {
	var errs []error
	for index := len(t.closers) - 1; index >= 0; index-- {
		errs = append(errs, t.closers[index]())
	}
	t.closers = nil
	return errors.Join(errs...)
}

// productD09CommandHistory is the opener of a production turn: the research recovery verifier of D09
// command receipts over the turn's retained command journal, bound to the turn's workflow, run,
// session and conversation. The journal is pinned as the host holds it, and the caller checks that
// pin against the receipt's committed head and count.
func productD09CommandHistory(profile piruntime.HostProfile, template piruntime.D09CellTemplate) productCommandHistoryOpener {
	return func(journal *audit.Journal, link productsession.TurnLink) (governedRecoveryCommandVerifier, error) {
		trace, err := journal.Trace()
		if err != nil {
			return nil, err
		}
		identity, err := recoveryFileIdentityDigest(link.CommandJournalPath, false)
		if err != nil {
			return nil, err
		}
		history, err := newProductionHistoricalBashHistory([]productionRecoveryCommandJournal{{Journal: journal, Path: link.CommandJournalPath,
			Pin:      productionHistoricalCommandPin{IdentityDigest: identity, Head: auditHeadHash(trace.Records), RecordCount: uint64(len(trace.Records))},
			Sessions: []productionRecoveryCommandSession{{RunID: link.RunID, SessionID: link.SessionID, ConversationID: link.ConversationID}}}},
			profile, template, link.WorkflowID, map[string]struct{}{link.ConversationID: {}})
		if err != nil {
			return nil, err
		}
		return history, nil
	}
}

// importedPrefixReplayPin is RecoveryPins.ImportedPrefixDigest for replaying a provider journal of
// records entries: a conversation's import record opens every non-empty journal, and an empty journal
// has no record to pin yet.
func importedPrefixReplayPin(digest string, records int) string {
	if records == 0 {
		return ""
	}
	return digest
}
