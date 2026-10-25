package main

import (
	"fmt"
	"io"
	"strings"

	"tbound/supervisor/internal/productprotocol"
)

// govMark starts every line of governance output (denials and their hints). Model and tool text is
// only ever printed behind a quote margin, so it cannot start a line with this mark.
const govMark = "!! governance"

// chatClean drops what a terminal would act on: C0 controls except newline and tab, DEL, C1
// controls (8-bit CSI), and the bidirectional overrides that reorder displayed text.
func chatClean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		case r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			return -1
		}
		return r
	}, s)
}

// chatLine is chatClean for a value that must stay on one line.
func chatLine(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(chatClean(s))
}

// chatQuote prints free text behind a margin, one margin per line.
func chatQuote(margin, text string) string {
	text = strings.TrimRight(chatClean(text), "\n")
	if text == "" {
		return ""
	}
	return margin + strings.ReplaceAll(text, "\n", "\n"+margin) + "\n"
}

func chatTurnLabel(id string) string {
	return chatLine(id[strings.LastIndex(id, "/")+1:])
}

func chatIsDenial(ev productprotocol.Event) bool {
	return (ev.Verdict != "" && !strings.EqualFold(ev.Verdict, "allow")) || ev.Hint != ""
}

func printEvent(w io.Writer, ev productprotocol.Event) {
	turn := chatTurnLabel(ev.TurnID)
	head := "[" + turn + "] "
	switch ev.Kind {
	case productprotocol.EventTurnAdmitted:
		fmt.Fprintf(w, "%sadmitted\n%s", head, chatQuote(head+"you | ", ev.Text))
	case productprotocol.EventTurnLinked:
		fmt.Fprintf(w, "%sstarted\n", head)
	case productprotocol.EventAssistantMessage:
		fmt.Fprint(w, chatQuote(head+"assistant | ", ev.Text))
	case productprotocol.EventToolCall:
		fmt.Fprintf(w, "%stool %s: %s\n", head, chatLine(ev.Tool), chatLine(ev.Verdict))
		if chatIsDenial(ev) {
			fmt.Fprintf(w, "%s%s: denied %s (%s)\n", head, govMark, chatLine(ev.Tool), chatLine(ev.ReasonCode))
			fmt.Fprint(w, chatQuote(head+govMark+" hint | ", ev.Hint))
		}
	case productprotocol.EventToolResult:
		fmt.Fprintf(w, "%sresult of %s\n%s", head, chatLine(ev.Tool), chatQuote(head+"result | ", ev.Text))
	case productprotocol.EventTurnCancelRequested:
		fmt.Fprintf(w, "%scancel requested\n", head)
	case productprotocol.EventTurnAwaitingReview:
		fmt.Fprintf(w, "%sawaiting review: %d changed path(s), candidate %s\n", head, len(ev.Paths), chatLine(ev.CandidateDigest))
		for _, p := range ev.Paths {
			fmt.Fprintf(w, "%s  %s\n", head, chatLine(p))
		}
	case productprotocol.EventTurnResolution:
		fmt.Fprintf(w, "%sreview decision recorded%s\n", head, chatSuffix(ev.Reason))
	case productprotocol.EventTurnTerminal:
		fmt.Fprintf(w, "%soutcome: %s%s\n", head, chatLine(ev.Outcome), chatSuffix(ev.Reason))
	default:
		fmt.Fprintf(w, "%sevent %s\n", head, chatLine(ev.Kind))
	}
}

func chatSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + chatLine(reason) + ")"
}

func printCandidate(w io.Writer, c *productprotocol.CandidateView) {
	fmt.Fprintf(w, "candidate %s: %d change(s)\n", chatLine(c.CandidateDigest), len(c.Changes))
	for _, change := range c.Changes {
		fmt.Fprintf(w, "  %s %s\n", chatLine(change.Op), chatLine(change.Path))
		fmt.Fprint(w, chatQuote("    | ", change.Diff))
		if change.Truncated {
			fmt.Fprintln(w, "    (diff truncated)")
		}
	}
}

func printBudget(w io.Writer, v *productprotocol.SessionView) {
	line := func(name string, limit, used, reserved int64) string {
		return fmt.Sprintf("%s %d used, %d reserved of %d", name, used, reserved, limit)
	}
	fmt.Fprintf(w, "budget: %s; %s; %s\n",
		line("host calls", v.Budget.HostCalls, v.Consumed.HostCalls, v.Reserved.HostCalls),
		line("tokens", v.Budget.Tokens, v.Consumed.Tokens, v.Reserved.Tokens),
		line("seconds", v.Budget.DurationSeconds, v.Consumed.DurationSeconds, v.Reserved.DurationSeconds))
}

func printSession(w io.Writer, v *productprotocol.SessionView, turns bool) {
	flags := "mode " + chatLine(v.PublicationMode)
	if v.ReadOnly {
		flags += ", read-only"
	}
	if v.Blocked {
		flags += ", BLOCKED"
	}
	fmt.Fprintf(w, "session %s: project %s, provider %s, %s, revision %d\n",
		chatLine(v.ID), chatLine(v.ProjectID), chatLine(v.ProviderProfile), flags, v.Revision)
	printBudget(w, v)
	if !turns {
		return
	}
	for _, t := range v.Turns {
		state := chatLine(t.State)
		if t.Outcome != "" {
			state += "/" + chatLine(t.Outcome) + chatSuffix(t.TerminalReason)
		}
		if t.CancelRequested {
			state += ", cancel requested"
		}
		if t.Resolution != "" {
			state += ", resolution " + chatLine(t.Resolution)
		}
		fmt.Fprintf(w, "  turn-%d %s: %s\n", t.Ordinal, state, chatLine(chatShorten(t.Prompt, 60)))
	}
}

func chatShorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
