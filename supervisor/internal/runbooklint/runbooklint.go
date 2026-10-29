// Package runbooklint reads the TBound command lines that the guest runbooks name. Each
// binary's test runs them through that binary's own dispatch and FlagSets, so a runbook cannot
// name a subcommand or a flag the code lacks. The package has no build tag.
package runbooklint

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Docs are the runbooks the lint reads, relative to the repository root.
var Docs = []string{
	"infra/guest/PROVISIONING-SIGNING.md",
	"infra/guest/operator/README.md",
}

// Command is one command line of a runbook.
type Command struct {
	File   string   // runbook path, relative to the repository root
	Line   int      // 1-based line number
	Binary string   // tbound, tbound-profile, tbound-run or tbound-op
	Words  []string // the words after the binary, with surrounding quotes removed
}

// shorthands are the names the runbooks use for a binary. TBP and TBPG are the tbound-profile
// variables of PROVISIONING-SIGNING.md (TBPG is `tbound-profile guest-evidence`), and $OP is the
// tbound-op variable of the operator README.
var shorthands = map[string][]string{
	"TBP":  {"tbound-profile"},
	"TBPG": {"tbound-profile", "guest-evidence"},
	"$OP":  {"tbound-op"},
}

var binaries = map[string]bool{"tbound": true, "tbound-profile": true, "tbound-run": true, "tbound-op": true}

var (
	codeSpan    = regexp.MustCompile("`([^`]+)`")
	tableVerb   = regexp.MustCompile("^\\|\\s*`([^`]+)`")
	placeholder = regexp.MustCompile(`<[^<>]*>`)
	comment     = regexp.MustCompile(`(^|\s)#.*$`)
	subcommand  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// Extract returns the command lines of the runbook at root/rel. Lines inside fenced code blocks
// are read whole. Elsewhere only inline code spans are read, so prose that names a binary is not
// a command. In the operator README's "## Verbs" table, the first code span of a row is a
// tbound-op verb. A command starts at the first word whose basename is a binary or a shorthand,
// and it ends at ';', '|', '&&', '||' or a '#' comment. A <placeholder> is one word, so <1|2> does
// not end a command. The first word after the binary must be a lowercase name, so a definition
// such as "TBP = ..." is not a command.
func Extract(root, rel string) ([]Command, error) {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cmds []Command
	inBlock, inVerbs := false, false
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inBlock = !inBlock
			continue
		}
		if strings.HasPrefix(line, "## ") {
			inVerbs = line == "## Verbs"
		}
		var texts []string
		switch {
		case inBlock:
			texts = []string{line}
		case inVerbs && strings.HasPrefix(line, "| `"):
			if m := tableVerb.FindStringSubmatch(line); m != nil {
				texts = []string{"tbound-op " + m[1]}
			}
		default:
			for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
				texts = append(texts, m[1])
			}
		}
		for _, text := range texts {
			for _, seg := range segments(text) {
				if c, ok := parse(seg); ok {
					c.File, c.Line = rel, n
					cmds = append(cmds, c)
				}
			}
		}
	}
	return cmds, sc.Err()
}

// Commands returns the commands of every doc in Docs that run binary.
func Commands(root, binary string) ([]Command, error) {
	var out []Command
	for _, rel := range Docs {
		cmds, err := Extract(root, rel)
		if err != nil {
			return nil, err
		}
		for _, c := range cmds {
			if c.Binary == binary {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// segments splits one code line or span at the shell separators the runbooks use.
func segments(text string) []string {
	text = placeholder.ReplaceAllStringFunc(text, func(p string) string {
		return strings.NewReplacer(" ", "_", "|", "_", ";", "_", "&", "_", "#", "_").Replace(p)
	})
	text = strings.NewReplacer(`\|`, "|", "&&", ";", "||", ";", "|", ";").Replace(text)
	var out []string
	for _, seg := range strings.Split(text, ";") {
		out = append(out, comment.ReplaceAllString(seg, ""))
	}
	return out
}

// parse returns the command in one segment, if the segment names a binary. A segment that starts
// a shell loop, or that defines a value with "=", names no command. The binary may follow "$(",
// "`" or "NAME=" in a substitution or assignment.
func parse(seg string) (Command, bool) {
	var words []string
	for _, w := range strings.Fields(seg) {
		words = append(words, strings.Trim(w, `'"`))
	}
	if len(words) == 0 || words[0] == "for" {
		return Command{}, false
	}
	for i, w := range words {
		if w == "=" {
			return Command{}, false
		}
		name := strings.TrimSuffix(path.Base(afterSubstitution(w)), ".exe")
		if i+1 >= len(words) || !isSubcommand(words[i+1]) {
			continue
		}
		if pre, ok := shorthands[name]; ok {
			return Command{Binary: pre[0], Words: append(append([]string{}, pre[1:]...), words[i+1:]...)}, true
		}
		if binaries[name] {
			return Command{Binary: name, Words: words[i+1:]}, true
		}
	}
	return Command{}, false
}

// afterSubstitution returns the part of a word after the last "=", "(" or backquote, so that
// SRC_DIGEST=$(tbound-run is read as tbound-run.
func afterSubstitution(w string) string {
	if i := strings.LastIndexAny(w, "=(`"); i >= 0 {
		return w[i+1:]
	}
	return w
}

// isSubcommand reports whether w can be a subcommand: a lowercase name that is not itself a binary.
func isSubcommand(w string) bool {
	_, short := shorthands[w]
	return subcommand.MatchString(w) && !binaries[w] && !short
}

// Flags returns the names of the --flags among words, without the dashes or any =value.
func Flags(words []string) []string {
	var out []string
	for _, w := range words {
		if len(w) > 2 && strings.HasPrefix(w, "--") {
			name := strings.TrimPrefix(w, "--")
			if i := strings.IndexByte(name, '='); i >= 0 {
				name = name[:i]
			}
			out = append(out, name)
		}
	}
	return out
}

// ParseReached reports whether err is the flag package's help sentinel. A subcommand returns it
// when -h reaches its FlagSet, so a true result means the subcommand exists and takes flags.
func ParseReached(err error) bool {
	return err != nil && strings.Contains(err.Error(), "flag: help requested")
}

// FlagDefined reports whether err, from parsing "--name=x -h" through a FlagSet, shows that the
// FlagSet defines the flag. An undefined flag fails first with "flag provided but not defined";
// a string flag takes x and reaches -h; a bool flag rejects x. The probe never runs the command.
func FlagDefined(err error) bool {
	return err != nil && !strings.Contains(err.Error(), "flag provided but not defined")
}

// TboundOpVerbs returns the verbs of the tbound-op dispatcher: the labels of the last
// "case $VERB in" block, which the script indents by two spaces.
func TboundOpVerbs(script string) []string {
	lines := strings.Split(script, "\n")
	start := -1
	for i, l := range lines {
		if l == "case $VERB in" {
			start = i
		}
	}
	var verbs []string
	if start < 0 {
		return verbs
	}
	for _, l := range lines[start+1:] {
		if l == "esac" {
			break
		}
		if m := verbLabel.FindStringSubmatch(l); m != nil {
			verbs = append(verbs, m[1])
		}
	}
	return verbs
}

var verbLabel = regexp.MustCompile(`^  ([a-z][a-z-]*)\)`)
