package proxystore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAddValidatesAndDefaultsName(t *testing.T) {
	s := New()
	for _, bad := range []string{"", "direct", "NONE", "ftp://h:1", "socks5://"} {
		if _, errAdd := s.Add("x", bad); errAdd == nil {
			t.Fatalf("Add(%q) accepted", bad)
		} else {
			var input *InputError
			if !errors.As(errAdd, &input) || input.Field != "url" {
				t.Fatalf("Add(%q) error = %v, want url InputError", bad, errAdd)
			}
		}
	}
	p, errAdd := s.Add("  ", " socks5://u:secret@10.0.0.1:1080 ")
	if errAdd != nil {
		t.Fatalf("Add: %v", errAdd)
	}
	if p.Name != "10.0.0.1:1080" || p.URL != "socks5://u:secret@10.0.0.1:1080" || p.ID == "" {
		t.Fatalf("proxy = %+v", p)
	}
	if p.Label() != "socks5://10.0.0.1:1080" {
		t.Fatalf("label = %q", p.Label())
	}
	if _, errDup := s.Add("again", "socks5://u:secret@10.0.0.1:1080"); !errors.Is(errDup, ErrDuplicate) {
		t.Fatalf("duplicate error = %v", errDup)
	}
	long := ""
	for range MaxName + 1 {
		long += "名"
	}
	if _, errName := s.Add(long, "http://10.0.0.2:8080"); errName == nil {
		t.Fatal("long name accepted")
	}
}

func TestUpdateAndDelete(t *testing.T) {
	s := New()
	a, _ := s.Add("A", "http://10.0.0.1:8080")
	b, _ := s.Add("B", "http://10.0.0.2:8080")

	name := " Hong   Kong "
	before, after, errUpdate := s.Update(a.ID, &name, nil)
	if errUpdate != nil || before.Name != "A" || after.Name != "Hong Kong" || after.URL != a.URL {
		t.Fatalf("rename: before=%+v after=%+v err=%v", before, after, errUpdate)
	}
	taken := b.URL
	if _, _, errDup := s.Update(a.ID, nil, &taken); !errors.Is(errDup, ErrDuplicate) {
		t.Fatalf("duplicate URL error = %v", errDup)
	}
	same := a.URL
	if _, _, errSame := s.Update(a.ID, nil, &same); errSame != nil {
		t.Fatalf("keeping own URL: %v", errSame)
	}
	moved := "socks5h://10.0.0.9:1080"
	if _, after, errMove := s.Update(a.ID, nil, &moved); errMove != nil || after.URL != moved {
		t.Fatalf("move: %+v %v", after, errMove)
	}
	if _, _, errMissing := s.Update("nope", &name, nil); !errors.Is(errMissing, ErrNotFound) {
		t.Fatalf("missing error = %v", errMissing)
	}
	if _, errDelete := s.Delete(b.ID); errDelete != nil {
		t.Fatalf("delete: %v", errDelete)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("list after delete = %+v", list)
	}
	if _, errDelete := s.Delete(b.ID); !errors.Is(errDelete, ErrNotFound) {
		t.Fatalf("second delete error = %v", errDelete)
	}
}

func TestPersistsAcrossConfigure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s := New()
	s.Configure(dir)
	p, errAdd := s.Add("HK", "socks5://u:pw@10.0.0.1:1080")
	if errAdd != nil {
		t.Fatalf("Add: %v", errAdd)
	}
	info, errStat := os.Stat(filepath.Join(dir, fileName))
	if errStat != nil {
		t.Fatalf("stat: %v", errStat)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %v, want 0600", mode)
	}
	added, errMissing := s.AddMissing([]string{"socks5://u:pw@10.0.0.1:1080", "http://10.0.0.3:3128", "direct", "http://10.0.0.3:3128"})
	if errMissing != nil || added != 1 {
		t.Fatalf("AddMissing = %d, %v", added, errMissing)
	}

	reloaded := New()
	reloaded.Configure(dir)
	list, loadErr := reloaded.List()
	if loadErr != "" || len(list) != 2 || list[0] != p || list[1].Name != "10.0.0.3:3128" {
		t.Fatalf("reloaded = %+v (%s)", list, loadErr)
	}
	if got, ok := list.Match(" socks5://u:pw@10.0.0.1:1080 "); !ok || got.ID != p.ID {
		t.Fatalf("Match = %+v %v", got, ok)
	}
	if _, ok := list.Match(""); ok {
		t.Fatal("empty URL matched")
	}
}

func TestUnreadableFileBlocksWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if errWrite := os.WriteFile(path, []byte("{broken"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	s := New()
	s.Configure(dir)
	if _, loadErr := s.List(); loadErr == "" {
		t.Fatal("no load error for a broken file")
	}
	if _, errAdd := s.Add("x", "http://10.0.0.1:8080"); errAdd == nil {
		t.Fatal("Add overwrote an unreadable file")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{broken" {
		t.Fatalf("file changed to %q", raw)
	}
	// Fixing the file and reconfiguring picks it up.
	if errWrite := os.WriteFile(path, []byte(`{"version":1,"proxies":[{"id":"a1","name":"A","url":"http://10.0.0.1:8080"}]}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	s.Configure(dir)
	list, loadErr := s.List()
	if loadErr != "" || len(list) != 1 || list[0].ID != "a1" {
		t.Fatalf("after fix = %+v (%s)", list, loadErr)
	}
}

func TestChecksPersistAndFollowTheURL(t *testing.T) {
	dir := t.TempDir()
	s := New()
	s.Configure(dir)
	p, _ := s.Add("HK", "socks5://10.0.0.1:1080")
	check := Check{OK: true, Egress: "socks5://10.0.0.1:1080", IP: "203.0.113.7", Country: "HK", City: "Kwai Chung", LatencyMS: 312, CheckedAt: "2026-11-12T14:00:00Z"}
	if errSet := s.SetCheck(p.ID, p.URL, check); errSet != nil {
		t.Fatalf("SetCheck: %v", errSet)
	}
	// A check of an address the proxy no longer has is dropped.
	if errSet := s.SetCheck(p.ID, "socks5://10.0.0.9:1080", Check{IP: "198.51.100.1"}); errSet != nil {
		t.Fatalf("stale SetCheck: %v", errSet)
	}
	if errSet := s.SetCheck("gone", p.URL, check); errSet != nil {
		t.Fatalf("SetCheck of a deleted proxy: %v", errSet)
	}
	if errSet := s.SetBuiltinCheck(BuiltinDirect, Check{OK: true, Egress: "direct", IP: "45.32.10.8", CheckedAt: "2026-11-12T14:01:00Z"}); errSet != nil {
		t.Fatalf("SetBuiltinCheck: %v", errSet)
	}
	if errSet := s.SetBuiltinCheck("elsewhere", Check{}); errSet == nil {
		t.Fatal("accepted an unknown built-in egress")
	}

	reloaded := New()
	reloaded.Configure(dir)
	list, _ := reloaded.List()
	if len(list) != 1 || list[0].LastCheck == nil || *list[0].LastCheck != check {
		t.Fatalf("reloaded check = %+v", list[0].LastCheck)
	}
	if got := reloaded.BuiltinChecks(); len(got) != 1 || got[BuiltinDirect].IP != "45.32.10.8" {
		t.Fatalf("built-in checks = %+v", got)
	}

	// Renaming keeps the check; a new URL clears it.
	name := "Hong Kong"
	if _, after, _ := reloaded.Update(p.ID, &name, nil); after.LastCheck == nil {
		t.Fatal("rename dropped the last check")
	}
	moved := "socks5://10.0.0.9:1080"
	if _, after, _ := reloaded.Update(p.ID, nil, &moved); after.LastCheck != nil {
		t.Fatalf("URL change kept the check: %+v", after.LastCheck)
	}
}
