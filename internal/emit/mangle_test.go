package emit

import "testing"

func TestMangle(t *testing.T) {
	for in, want := range map[string]string{
		"main":       "main",
		"myVar":      "myVar",
		"my_var":     "my_1var",
		"main$1":     "main__1",
		"init$guard": "init__guard",
		"type":       "r#type",
		"self":       "self_0",
		"_":          "_1",
		"héllo":      "h_ue9_llo",
		"Map[int]":   "Map_u5b_int_u5d_",
	} {
		if got := mangle(in); got != want {
			t.Errorf("mangle(%q) = %q, want %q", in, got, want)
		}
	}
	// Distinct names stay distinct even when they look alike.
	seen := map[string]string{}
	for _, in := range []string{"a_b", "a__b", "a_1b", "a$b", "a$$b", "a_$b"} {
		m := mangle(in)
		if prev, dup := seen[m]; dup {
			t.Errorf("mangle(%q) == mangle(%q) == %q", in, prev, m)
		}
		seen[m] = in
	}
}

func TestNamespace(t *testing.T) {
	ns := namespace{}
	if a, b := ns.claim("x"), ns.claim("x"); a != "x" || b != "x_02" {
		t.Errorf("claim: got %q, %q", a, b)
	}
	if k := ns.claim("r#type"); k != "r#type" {
		t.Errorf("claim keyword: %q", k)
	}
	if k := ns.claim("r#type"); k != "type_02" {
		t.Errorf("claim keyword twice: %q", k)
	}
}
