package taskaccept

import "testing"

func TestMutationControlsRejectAllFourCorruptions(t *testing.T) {
	good := solution(t, writeOp("src/main.go", "correct-answer\n"))
	acc := accept(t, Check{Op: OpFileSHA256, Path: "src/main.go", Digest: DigestOf([]byte("correct-answer\n"))})
	results := MutationControls(fixtureSpec(), acc, good, nil)
	if len(results) != 4 {
		t.Fatalf("got %d controls, want 4: %+v", len(results), results)
	}
	for _, r := range results {
		if !r.Rejected || r.Error != "" {
			t.Errorf("control failed: %+v", r)
		}
	}
}

func TestMutationControlsCatchWeakOracle(t *testing.T) {
	good := solution(t, writeOp("src/main.go", "correct-answer\nignored suffix\n"))
	acc := accept(t, Check{Op: OpContains, Path: "src/main.go", Text: "correct"})
	for _, r := range MutationControls(fixtureSpec(), acc, good, nil) {
		if r.Mutation == "truncated" {
			if r.Rejected {
				t.Fatal("weak oracle accepted truncation but control passed")
			}
			return
		}
	}
	t.Fatal("no truncation control")
}

func TestMutationControlsRunnerErrorIsNotRejection(t *testing.T) {
	good := solution(t, writeOp("src/main.go", "function answer() { return 42; }\n"))
	acc := accept(t, Check{Op: OpNodeTest, Path: "src/main.go", Text: "require('node:assert').equal(answer(), 42);"})
	for _, r := range MutationControls(fixtureSpec(), acc, good, failingRunner{}) {
		if r.Mutation != "another-path" && (r.Rejected || r.Error == "") {
			t.Errorf("runner error counted as rejection: %+v", r)
		}
	}
}

func TestMutationControlsWithoutFunctionDeclarationFail(t *testing.T) {
	good := solution(t, writeOp("src/main.go", "const answer = () => 42;\n"))
	acc := accept(t, Check{Op: OpNodeTest, Path: "src/main.go", Text: "require('node:assert').equal(answer(), 42);"})
	results := MutationControls(fixtureSpec(), acc, good, failingRunner{})
	if len(results) != 1 || results[0].Error == "" || results[0].Rejected {
		t.Fatalf("non-behavioural wrong value not reported: %+v", results)
	}
}
