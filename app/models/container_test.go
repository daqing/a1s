package models

import "testing"

func TestEnvValueScanRoundTrip(t *testing.T) {
	env := Env{"FOO": "bar", "BAZ": "qux"}

	stored, err := env.Value()
	if err != nil {
		t.Fatalf("Value() failed: %v", err)
	}

	var back Env
	if err := back.Scan(stored); err != nil {
		t.Fatalf("Scan(%v) failed: %v", stored, err)
	}

	if len(back) != 2 || back["FOO"] != "bar" || back["BAZ"] != "qux" {
		t.Fatalf("round-trip mismatch: %#v", back)
	}
}

func TestArgsValueScanRoundTrip(t *testing.T) {
	args := Args{"-g", "daemon off;"}

	stored, err := args.Value()
	if err != nil {
		t.Fatalf("Value() failed: %v", err)
	}

	var back Args
	if err := back.Scan(stored); err != nil {
		t.Fatalf("Scan(%v) failed: %v", stored, err)
	}

	if len(back) != 2 || back[0] != "-g" || back[1] != "daemon off;" {
		t.Fatalf("round-trip mismatch: %#v", back)
	}
}

func TestJSONBEmptyValues(t *testing.T) {
	envValue, err := Env(nil).Value()
	if err != nil || envValue != "{}" {
		t.Fatalf("nil Env must serialize to {}, got %v, %v", envValue, err)
	}

	argsValue, err := Args(nil).Value()
	if err != nil || argsValue != "[]" {
		t.Fatalf("nil Args must serialize to [], got %v, %v", argsValue, err)
	}

	var env Env
	if err := env.Scan(nil); err != nil {
		t.Fatalf("Scan(nil) must leave the map untouched, got %v", err)
	}
}
