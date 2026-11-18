package main

import (
	"context"
	"errors"
	"testing"
)

func federated() Config {
	return Config{workloadPoolId: "pool", providerId: "prov", gcpProjectId: "123", serviceAccountEmail: "sa@x"}
}

func TestResolveWorkloadIdentityToken(t *testing.T) {
	orig := mintWorkloadIdentityToken
	defer func() { mintWorkloadIdentityToken = orig }()
	calls := 0
	mintWorkloadIdentityToken = func(_ context.Context, name string) (string, error) {
		calls++
		return "tok-" + name, nil
	}
	ctx := context.Background()

	t.Run("no identity keeps existing flow", func(t *testing.T) {
		calls = 0
		c := federated()
		if err := c.resolveWorkloadIdentityToken(ctx); err != nil || calls != 0 || c.OidcIdToken != "" {
			t.Fatalf("err=%v calls=%d token=%q", err, calls, c.OidcIdToken)
		}
	})
	t.Run("handle without name keeps existing flow", func(t *testing.T) {
		calls = 0
		c := federated()
		c.WorkloadIdentityHandle = "h"
		if err := c.resolveWorkloadIdentityToken(ctx); err != nil || calls != 0 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})
	t.Run("connector token is not replaced", func(t *testing.T) {
		calls = 0
		c := federated()
		c.OidcIdToken, c.WorkloadIdentityHandle, c.WorkloadIdentityName = "connector", "h", "x"
		if err := c.resolveWorkloadIdentityToken(ctx); err != nil || calls != 0 || c.OidcIdToken != "connector" {
			t.Fatalf("err=%v calls=%d token=%q", err, calls, c.OidcIdToken)
		}
	})
	t.Run("identity mints token", func(t *testing.T) {
		calls = 0
		c := federated()
		c.WorkloadIdentityHandle, c.WorkloadIdentityName = "h", "OIDC_GCP_TOKEN"
		if err := c.resolveWorkloadIdentityToken(ctx); err != nil || calls != 1 || c.OidcIdToken != "tok-OIDC_GCP_TOKEN" {
			t.Fatalf("err=%v calls=%d token=%q", err, calls, c.OidcIdToken)
		}
	})
	t.Run("default identity without federation config is a no-op", func(t *testing.T) {
		calls = 0
		c := Config{WorkloadIdentityHandle: "h", WorkloadIdentityName: defaultWorkloadIdentityName}
		if err := c.resolveWorkloadIdentityToken(ctx); err != nil || calls != 0 || c.OidcIdToken != "" {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})
	t.Run("named identity without federation config fails clearly", func(t *testing.T) {
		c := Config{WorkloadIdentityHandle: "h", WorkloadIdentityName: "OIDC_GCP_TOKEN"}
		if err := c.resolveWorkloadIdentityToken(ctx); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("default identity mint failure is fatal", func(t *testing.T) {
		mintWorkloadIdentityToken = func(context.Context, string) (string, error) { return "", errors.New("boom") }
		defer func() {
			mintWorkloadIdentityToken = func(_ context.Context, n string) (string, error) { calls++; return "tok-" + n, nil }
		}()
		c := federated()
		c.WorkloadIdentityHandle, c.WorkloadIdentityName = "h", defaultWorkloadIdentityName
		if err := c.resolveWorkloadIdentityToken(ctx); err == nil || c.OidcIdToken != "" {
			t.Fatalf("expected mint error, err=%v token=%q", err, c.OidcIdToken)
		}
	})
	t.Run("named identity mint failure is fatal", func(t *testing.T) {
		mintWorkloadIdentityToken = func(context.Context, string) (string, error) { return "", errors.New("boom") }
		defer func() { mintWorkloadIdentityToken = orig }()
		c := federated()
		c.WorkloadIdentityHandle, c.WorkloadIdentityName = "h", "OIDC_GCP_TOKEN"
		if err := c.resolveWorkloadIdentityToken(ctx); err == nil {
			t.Fatal("expected error")
		}
	})
}
