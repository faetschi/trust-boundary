//go:build linux

package productsession

import (
	"slices"
	"testing"
)

func TestUnsettledTurnsNamesUnresolvedAndUnknownTurnsAndAppendsNothing(t *testing.T) {
	c, path := newCatalog(t)
	reg := c.st.reg

	admitted := mustSubmit(t, c, uid1, mustSession(t, c, uid1, "alpha", "review", bigBudget, "s-open").ID, 0, "k-open", "p")
	unknown := linkedTurn(t, c, "unknown")
	if err := c.MarkTurnUnknown(unknown.ID, "no verifiable disposition"); err != nil {
		t.Fatal(err)
	}
	abandoned := mustSubmit(t, c, uid1, mustSession(t, c, uid1, "beta", "review", bigBudget, "s-done").ID, 0, "k-done", "p")
	if err := c.AbandonUnlinkedTurn(abandoned.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := UnsettledTurns(path, reg); err == nil {
		t.Fatal("the catalog was read while the service holds it open")
	}
	records := recordCount(t, c)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	turns, err := UnsettledTurns(path, reg)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, turn := range turns {
		got = append(got, turn.ID+" "+string(turn.State))
	}
	want := []string{admitted.ID + " " + string(StateAdmitted), unknown.ID + " " + string(StateUnknown)}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("unsettled turns %v, want %v", got, want)
	}

	reopened := openAt(t, path, reg)
	if n := recordCount(t, reopened); n != records+1 {
		t.Fatalf("the journal has %d records, want the %d written before plus the reopen's own epoch record: the read appended", n, records)
	}
}
