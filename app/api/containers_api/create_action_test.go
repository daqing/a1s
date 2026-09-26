package containers_api

import (
	"testing"

	"github.com/daqing/a1s/app/models"
)

func TestNormalizeCreateRequestRequiresImage(t *testing.T) {
	if _, err := normalizeCreateRequest(&createRequest{}); err == nil {
		t.Fatalf("expected an error for a missing image")
	}
}

func TestNormalizeCreateRequestDefaults(t *testing.T) {
	vals, err := normalizeCreateRequest(&createRequest{Image: "nginx", Args: []string{}, Env: map[string]string{"K": "V"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if vals["restart_policy"] != "no" {
		t.Fatalf("expected default restart_policy no, got %v", vals["restart_policy"])
	}
	if vals["status"] != models.ContainerPending {
		t.Fatalf("expected pending status, got %v", vals["status"])
	}

	name, ok := vals["name"].(string)
	if !ok || name == "" || len(name) < 3 {
		t.Fatalf("expected a generated name, got %v", vals["name"])
	}
}

func TestNormalizeCreateRequestKeepsValidName(t *testing.T) {
	vals, err := normalizeCreateRequest(&createRequest{Name: "web-1", Image: "nginx"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if vals["name"] != "web-1" {
		t.Fatalf("expected name web-1, got %v", vals["name"])
	}
}

func TestNormalizeCreateRequestRejectsBadName(t *testing.T) {
	for _, name := range []string{"-lead", ".lead", "a b", "name!"} {
		if _, err := normalizeCreateRequest(&createRequest{Name: name, Image: "nginx"}); err == nil {
			t.Fatalf("expected an error for name %q", name)
		}
	}
}

func TestNormalizeRestartPolicy(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: "no"},
		{in: "no", want: "no"},
		{in: "always", want: "always"},
		{in: "unless-stopped", want: "unless-stopped"},
		{in: "on-failure", want: "on-failure"},
		{in: "on-failure:3", want: "on-failure:3"},
		{in: "on-failure:x", wantErr: true},
		{in: "on-failure:-1", wantErr: true},
		{in: "always:2", wantErr: true},
		{in: "sometimes", wantErr: true},
	}

	for _, tc := range cases {
		got, err := normalizeRestartPolicy(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("expected error for %q, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("for %q expected %q, got %q", tc.in, tc.want, got)
		}
	}
}

func TestNormalizeCreateRequestCarriesArgsAndEnv(t *testing.T) {
	vals, err := normalizeCreateRequest(&createRequest{
		Image: "nginx",
		Args:  []string{"-g", "daemon off;"},
		Env:   map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	args, ok := vals["args"].(models.Args)
	if !ok || len(args) != 2 {
		t.Fatalf("expected typed args, got %#v", vals["args"])
	}

	env, ok := vals["env"].(models.Env)
	if !ok || env["FOO"] != "bar" {
		t.Fatalf("expected typed env, got %#v", vals["env"])
	}
}
