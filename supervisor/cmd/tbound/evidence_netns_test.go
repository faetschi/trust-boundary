package main

import (
	"os"
	"path/filepath"
	"testing"
)

// netnsHeldIn must see a namespace held by a process, a thread, an open descriptor, a mount in the reader's own mount
// namespace and a mount in ANOTHER mount namespace; anything it cannot inspect refuses the proof.
func fakeProc(t *testing.T) (root string, link func(rel, target string), write func(rel, content string)) {
	t.Helper()
	root = t.TempDir()
	link = func(rel, target string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks are not available here: %v", err)
		}
	}
	write = func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestNetnsHeldInSeesEveryHolder(t *testing.T) {
	root, link, write := fakeProc(t)
	for _, pid := range []string{"1", "200", "300"} {
		link(pid+"/task/"+pid+"/ns/net", "net:[4026531840]")
		link(pid+"/ns/mnt", "mnt:[40265318"+pid+"]")
		write(pid+"/mountinfo", "36 35 0:4 / / rw - ext4 /dev/sda rw\n")
		_ = os.MkdirAll(filepath.Join(root, pid, "fd"), 0o755)
	}
	if held, err := netnsHeldIn(root, []uint64{4026532001}); err != nil || held {
		t.Fatalf("nothing holds it: held=%v err=%v", held, err)
	}
	// An open descriptor of a helper whose threads are all in the host namespace.
	link("200/fd/7", "net:[4026532001]")
	if held, err := netnsHeldIn(root, []uint64{4026532001}); err != nil || !held {
		t.Fatalf("an fd holder was missed: held=%v err=%v", held, err)
	}
	os.Remove(filepath.Join(root, "200/fd/7"))
	// A bind mount that only another mount namespace sees.
	write("300/mountinfo", "36 35 0:4 net:[4026532001] /run/netns/x rw - nsfs nsfs rw\n")
	if held, err := netnsHeldIn(root, []uint64{4026532001}); err != nil || !held {
		t.Fatalf("a foreign mount namespace bind mount was missed: held=%v err=%v", held, err)
	}
}

func TestNetnsHeldInRefusesWhatItCannotInspect(t *testing.T) {
	root, link, write := fakeProc(t)
	link("1/task/1/ns/net", "net:[4026531840]")
	link("1/ns/mnt", "mnt:[1]")
	write("1/mountinfo", "x\n")
	_ = os.MkdirAll(filepath.Join(root, "1", "fd"), 0o755)
	// A process whose mount namespace link or mountinfo is missing while the process exists.
	link("2/task/2/ns/net", "net:[4026531840]")
	_ = os.MkdirAll(filepath.Join(root, "2", "fd"), 0o755)
	write("2/mountinfo", "x\n")
	if _, err := netnsHeldIn(root, []uint64{4026532001}); err == nil {
		t.Error("a process without a readable mount namespace link was read as not holding it")
	}
	if _, err := netnsHeldIn(t.TempDir(), []uint64{1}); err == nil {
		t.Error("an empty /proc was read as not held")
	}
}
