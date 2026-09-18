package load

import "testing"

func TestLoadHello(t *testing.T) {
	res, err := Load("", "./testdata/hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pkgs) != 1 || res.Pkgs[0] == nil {
		t.Fatalf("got %d packages, want 1", len(res.Pkgs))
	}
	main := res.Pkgs[0].Func("main")
	if main == nil || len(main.Blocks) == 0 {
		t.Fatal("main.main missing or has no body")
	}
	// hello.go only builds with the rustygo tag set.
	if res.Pkgs[0].Func("tagged") == nil {
		t.Fatal("file guarded by the rustygo build tag was not selected")
	}
}
