//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/user"
	"strconv"

	"tbound/supervisor/internal/productprovider"
)

const productProviderUsage = `Usage:
  tbound provider list
  tbound provider setup --profile ID --service-user NAME [--from-file PATH] [--replace]
  tbound provider status --profile ID
  tbound provider check --profile ID --offline
  tbound provider check --profile ID --live --max-tokens N --yes
  tbound provider rotate --profile ID --service-user NAME [--from-file PATH]
  tbound provider remove --profile ID
Keys come only from a no-echo terminal prompt or a caller-owned protected file.
Default: /etc/tbound-product/credentials/<profile-id>.key (service owner; directory 0700; file 0600).
Live check sends exactly one request. No price table: monetary ceilings unsupported.
Exit codes: 0 success, 2 usage/refusal, 3 credential/provider check failed, 1 other.
`

type providerCommandError struct {
	message string
	code    int
}

func (e *providerCommandError) Error() string { return e.message }
func (e *providerCommandError) ExitCode() int { return e.code }
func providerError(code int, message string) error {
	return &providerCommandError{message: message, code: code}
}

type providerSecretReader interface{ ReadSecret() ([]byte, error) }
type providerCredentialStore interface {
	Install(string, int, int, []byte, bool, bool) (productprovider.Rotation, error)
	Status(string) (productprovider.Status, error)
	Check(context.Context, string, uint64) (productprovider.CheckResult, error)
	Remove(string) error
}
type providerCommand struct {
	store      providerCredentialStore
	terminal   providerSecretReader
	readSource func(string) ([]byte, error)
	lookupUser func(string) (int, int, error)
}

func runProvider(args []string, out, stderr io.Writer) error {
	c := providerCommand{store: productprovider.NewStore(), terminal: providerTerminal{}, readSource: productprovider.ReadSource, lookupUser: providerServiceUser}
	return c.run(context.Background(), args, out, stderr)
}

func providerServiceUser(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, errors.New("service account not found")
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr != nil || gidErr != nil || uid < 0 || gid < 0 {
		return 0, 0, errors.New("invalid service account identity")
	}
	return uid, gid, nil
}

func providerStorageError(err error) error {
	switch {
	case errors.Is(err, productprovider.ErrExists):
		return providerError(2, productprovider.ErrExists.Error())
	case errors.Is(err, productprovider.ErrAbsent):
		return providerError(2, productprovider.ErrAbsent.Error())
	case errors.Is(err, productprovider.ErrCredential):
		return providerError(3, productprovider.ErrCredential.Error())
	default:
		return providerError(1, "credential storage operation failed; check administrative permissions and disk space")
	}
}

func (c providerCommand) run(ctx context.Context, args []string, out, stderr io.Writer) error {
	usage := func() error {
		_, _ = io.WriteString(stderr, productProviderUsage)
		return providerError(2, "invalid provider command or flags")
	}
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "--help" || args[0] == "help" {
		_, err := io.WriteString(out, productProviderUsage)
		return providerWriteError(err)
	}
	if args[0] == "list" {
		if len(args) != 1 {
			return usage()
		}
		return providerWriteError(json.NewEncoder(out).Encode(productprovider.Advertised()))
	}
	command := args[0]
	if command != "setup" && command != "rotate" && command != "status" && command != "check" && command != "remove" {
		return usage()
	}
	f := flag.NewFlagSet("provider "+command, flag.ContinueOnError)
	f.SetOutput(io.Discard) // flag errors can contain caller-supplied secret text.
	id := f.String("profile", "", "qualified profile ID")
	var serviceUser, source string
	var replace, offline, live, yes bool
	var maxTokens uint64
	if command == "setup" || command == "rotate" {
		f.StringVar(&serviceUser, "service-user", "", "product service account")
		f.StringVar(&source, "from-file", "", "protected API key file")
		if command == "setup" {
			f.BoolVar(&replace, "replace", false, "replace existing key")
		}
	}
	if command == "check" {
		f.BoolVar(&offline, "offline", false, "configuration only")
		f.BoolVar(&live, "live", false, "one provider exchange")
		f.BoolVar(&yes, "yes", false, "authorize one bounded exchange")
		f.Uint64Var(&maxTokens, "max-tokens", 0, "completion-token ceiling")
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(out, productProviderUsage)
			return providerWriteError(err)
		}
		return usage()
	}
	if f.NArg() != 0 {
		return usage()
	}
	p, ok := productprovider.Lookup(*id)
	if !ok {
		return usage()
	}
	switch command {
	case "setup", "rotate":
		if serviceUser == "" {
			return usage()
		}
		uid, gid, err := c.lookupUser(serviceUser)
		if err != nil {
			return providerError(2, "service account not found or invalid; create the product service account first")
		}
		var key []byte
		if source != "" {
			key, err = c.readSource(source)
		} else {
			key, err = c.terminal.ReadSecret()
		}
		if err != nil {
			return providerError(3, "credential input refused; use a no-echo terminal or a caller-owned regular file with mode 0400/0600")
		}
		defer clear(key)
		rotation, err := c.store.Install(*id, uid, gid, key, replace || command == "rotate", command == "rotate")
		if err != nil {
			return providerStorageError(err)
		}
		if command == "rotate" {
			_, err = fmt.Fprintf(out, "rotated old_fingerprint=%s new_fingerprint=%s\n", rotation.OldFingerprint, rotation.NewFingerprint)
		} else {
			_, err = fmt.Fprintf(out, "configured fingerprint=%s\n", rotation.NewFingerprint)
		}
		return providerWriteError(err)
	case "status":
		status, err := c.store.Status(*id)
		if err != nil {
			return providerStorageError(err)
		}
		return providerWriteError(json.NewEncoder(out).Encode(status))
	case "remove":
		if err := c.store.Remove(*id); err != nil {
			return providerStorageError(err)
		}
		_, err := fmt.Fprintln(out, "credential removed (provider-side revocation is separate)")
		return providerWriteError(err)
	case "check":
		if offline == live || (offline && (yes || maxTokens != 0)) || (live && (!yes || maxTokens < p.MinCompletionTokens || maxTokens > p.MaxCompletionTokens)) {
			return usage()
		}
		if offline {
			status, err := c.store.Status(*id)
			if err != nil {
				return providerStorageError(err)
			}
			if !status.Present {
				return providerError(3, "credential absent; run provider setup")
			}
			_, err = fmt.Fprintf(out, "offline configuration valid fingerprint=%s; %s\n", status.Fingerprint, p.Pricing)
			return providerWriteError(err)
		}
		result, err := c.store.Check(ctx, *id, maxTokens)
		if err != nil {
			return providerError(3, err.Error())
		}
		return providerWriteError(json.NewEncoder(out).Encode(result))
	}
	return usage()
}

func providerWriteError(err error) error {
	if err != nil {
		return providerError(1, "could not write provider command output")
	}
	return nil
}
