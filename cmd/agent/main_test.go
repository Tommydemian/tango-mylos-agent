package main

import "testing"

func TestParseCLIDate(t *testing.T) {
	ok := map[string]string{
		"01/09/2026":  "2026-09-01", // DD/MM/AAAA, como se escribe en Tango
		"09/09/2026":  "2026-09-09",
		"2026-09-01":  "2026-09-01",
		" 31/12/2025": "2025-12-31",
	}
	for in, want := range ok {
		got, err := parseCLIDate(in)
		if err != nil {
			t.Errorf("parseCLIDate(%q): %v", in, err)
			continue
		}
		if got.Format("2006-01-02") != want {
			t.Errorf("parseCLIDate(%q) = %s, esperaba %s", in, got.Format("2006-01-02"), want)
		}
	}
	for _, in := range []string{"", "ayer", "2026/09/01", "31/31/2026", "09-01-2026"} {
		if _, err := parseCLIDate(in); err == nil {
			t.Errorf("parseCLIDate(%q) deberia fallar", in)
		}
	}
}

func TestRunComandoDesconocido(t *testing.T) {
	if err := run([]string{"no-existe"}); err == nil {
		t.Fatal("esperaba error")
	}
	if err := run(nil); err == nil {
		t.Fatal("esperaba error sin comando")
	}
	if err := run([]string{"version"}); err != nil {
		t.Fatalf("version: %v", err)
	}
}
