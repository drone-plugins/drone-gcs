package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// addonHcliPath is where the K8s init step copies hcli (shared /addon volume); step containers do not always
// have /addon/bin on PATH, so it is the fallback when hcli is not found on PATH.
const addonHcliPath = "/addon/bin/hcli"

// hcliBinary returns the hcli executable to run: the one on PATH, else the addon copy.
func hcliBinary() string {
	if p, err := exec.LookPath("hcli"); err == nil {
		return p
	}
	return addonHcliPath
}

// defaultWorkloadIdentityName is the identity the platform seeds for OOTB steps when no explicit identity
// is declared. It is injected for every step, so it must never change behavior of a step that did not
// opt into workload identity federation.
const defaultWorkloadIdentityName = "default_identity"

// mintWorkloadIdentityToken asks the in-pod broker (through hcli) for an OIDC token. The workload token
// never reaches this process; only the opaque HARNESS_WI_HANDLE does. Overridable for tests.
var mintWorkloadIdentityToken = func(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, hcliBinary(), "identity", "token", "--name", name).Output()
	if err != nil {
		return "", fmt.Errorf("hcli identity token failed for identity %q: %w", name, err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("hcli identity token returned an empty token for identity %q", name)
	}
	return token, nil
}

// oidcFederationConfigured reports whether every GCP workload identity federation parameter other than
// the ID token itself is present.
func (c *Config) oidcFederationConfigured() bool {
	return c.workloadPoolId != "" && c.providerId != "" && c.gcpProjectId != "" && c.serviceAccountEmail != ""
}

// resolveWorkloadIdentityToken fills OidcIdToken from the step's workload identity. It is a no-op when no
// identity is delivered, when a connector-minted token is already present, or when the default identity is
// delivered but the step did not configure GCP federation.
func (c *Config) resolveWorkloadIdentityToken(ctx context.Context) error {
	if c.WorkloadIdentityHandle == "" || c.WorkloadIdentityName == "" || c.OidcIdToken != "" {
		return nil
	}
	isDefault := c.WorkloadIdentityName == defaultWorkloadIdentityName
	if !c.oidcFederationConfigured() {
		if isDefault {
			return nil
		}
		return fmt.Errorf("workload identity %q is declared but the OIDC pool, provider, project number and "+
			"service account email are not all set", c.WorkloadIdentityName)
	}
	token, err := mintWorkloadIdentityToken(ctx, c.WorkloadIdentityName)
	if err != nil {
		// The platform only delivers the identity (and withholds the connector token) when this image is expected to
		// mint through it, so a failure here is a real error, not something to paper over with another auth flow.
		return fmt.Errorf("failed to mint token from workload identity %q: %w", c.WorkloadIdentityName, err)
	}
	c.OidcIdToken = token
	log.Printf("minted OIDC token from workload identity %q", c.WorkloadIdentityName)
	return nil
}
