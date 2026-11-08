//go:build linux

package main

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/researchroute"
)

// productionProviderRoute is the service's provider route, fixed for the life of
// the process by the provider_route member of the digest-pinned service config.
// The zero value is the engineering route (OpenCode Go): the fixed model and
// endpoint, no spend accounting, and the key file
// piruntime.D09ManagedPiProviderCredentialPath. It never reads a research file
// and never opens a ledger.
//
// The research route holds the opened research bundle: the frozen profile, the
// route built from it, and the durable USD ledger, whose lock the service keeps
// until Close.
type productionProviderRoute struct {
	research *researchroute.Bundle
}

// openProductionProviderRoute resolves the provider_route of the service config.
// "engineering" touches nothing. "research" opens the bundle at paths and
// refuses unless the profile is frozen, the key and the ledger configuration are
// secure, and the genesis ledger exists. It also refuses a workflow host-call
// limit above the profile's max_requests_per_unit: the workflow ledger enforces
// workflow_max_host_calls durably, so it bounds the requests of one unit.
func openProductionProviderRoute(config piServiceConfig, paths researchroute.Paths) (productionProviderRoute, error) {
	switch config.ProviderRoute {
	case piruntime.D09ManagedPiProviderRouteEngineering:
		return productionProviderRoute{}, nil
	case piruntime.D09ManagedPiProviderRouteResearch:
		bundle, err := researchroute.Open(paths)
		if err != nil {
			return productionProviderRoute{}, fmt.Errorf("open the research provider route: %w", err)
		}
		if config.WorkflowMaxHostCalls > bundle.MaxRequestsPerUnit() {
			_ = bundle.Close()
			return productionProviderRoute{}, fmt.Errorf(
				"workflow_max_host_calls %d exceeds the research profile max_requests_per_unit %d",
				config.WorkflowMaxHostCalls, bundle.MaxRequestsPerUnit())
		}
		return productionProviderRoute{research: bundle}, nil
	default:
		return productionProviderRoute{}, fmt.Errorf("provider_route %q is not engineering or research", config.ProviderRoute)
	}
}

// IsResearch reports whether the route is the research route.
func (r productionProviderRoute) IsResearch() bool { return r.research != nil }

// Route is the wire route to pin: the engineering profile or the route built from
// the frozen research profile, each with the Reasoning mark of the thesis governed
// and corpus path (decision D50). Product turns never use this route.
func (r productionProviderRoute) Route() openrouter.Route {
	if r.research == nil {
		return openrouter.Route{}.WithReasoning(true)
	}
	return r.research.Route().WithReasoning(true)
}

// Model is the provider model id the route pins.
func (r productionProviderRoute) Model() string { return r.Route().Resolved().Model }

// Spend is the USD accountant of the route, or nil on the engineering route.
func (r productionProviderRoute) Spend() providerbridge.SpendAccountant {
	if r.research == nil {
		return nil
	}
	return r.research
}

// ProviderProfileDigest is the provider-profile digest that the workflow runtime
// pin commits to. On the engineering route it is the host profile's digest,
// unchanged. On the research route it also commits to the frozen research
// profile, so a changed profile, price table or provider pin changes the pin.
func (r productionProviderRoute) ProviderProfileDigest(hostDigest string) string {
	if r.research == nil {
		return hostDigest
	}
	return r.research.ProviderProfileDigest(hostDigest)
}

// Close releases the research ledger. It is a no-op on the engineering route.
func (r productionProviderRoute) Close() error {
	if r.research == nil {
		return nil
	}
	return r.research.Close()
}

// conversationFactory is the credential and transport source of the route. The
// engineering route keeps the fixed service key-file factory; the research route
// reads openrouter.key (never the environment) for every conversation.
func (r productionProviderRoute) conversationFactory() func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error) {
	if r.research == nil {
		return nil
	}
	bundle := r.research
	return func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		if !config.Route.IsResearch() || config.Route.WithReasoning(false) != bundle.Route() || config.Spend == nil {
			return nil, errors.New("research conversation is not bound to the opened research route and ledger")
		}
		credentials, err := bundle.Credentials()
		if err != nil {
			return nil, err
		}
		return providerbridge.NewFromCredentials(config, credentials, journal)
	}
}

// BindProviderRoute attaches the host-opened provider route to the authority. It
// is called once, when the authority is created. The zero route leaves the
// authority exactly as the engineering route has always been.
func (a *SessionOperationAuthority) BindProviderRoute(route productionProviderRoute) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providerRoute = route.Route()
	a.providerSpend = route.Spend()
	a.providerFactory = route.conversationFactory()
}

// applyProviderRoute overwrites any caller-supplied route and spend hook with the
// host-bound ones, as the authority already does for the workflow-ledger
// callbacks. An authority whose executor releases command output (a product turn)
// marks the route, so the request tells the provider what the executor returns.
func (a *SessionOperationAuthority) applyProviderRoute(config providerbridge.Config) providerbridge.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	config.Route, config.Spend = a.providerRoute, a.providerSpend
	if a.commandOutputBytes > 0 {
		config.Route = config.Route.WithCommandOutput(true)
	}
	config.HintProvider = a.hintProvider
	return config
}

// currentProviderRoute is the resolved route the authority verifies evidence against.
func (a *SessionOperationAuthority) currentProviderRoute() openrouter.Route {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.providerRoute.Resolved()
}
