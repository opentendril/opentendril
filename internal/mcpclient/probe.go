package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// OwnerProbe is what the unauthenticated health surface reports about who
// owns the Stem answering at an address.
//
// Reached distinguishes "nothing answered" from "something answered but did
// not publish an owner". Those are different conditions with different
// remedies, and collapsing them would make a caller unable to tell an absent
// Stem from an ungoverned one.
type OwnerProbe struct {
	// Address is what was probed, reported so a caller can name it.
	Address string
	// Reached is true when a Stem answered and its report decoded.
	Reached bool
	// Owner is the published owner, nil when none was published.
	Owner *int
	// Err is populated when readiness could not be authenticated or decoded.
	Err error
}

// ProbeOwner asks the resolved address who owns the Stem there.
//
// Shared by mode selection and by hardiness so the two cannot drift into
// disagreeing about whether another principal is serving on this host.
func ProbeOwner(ctx context.Context) OwnerProbe {
	address := ResolveStemAddress("")
	probe := ProbeOwnerAt(ctx, "http://"+address)
	// Preserve the legacy helper's host:port report for hardiness and mode
	// selection callers. ProbeOwnerAt reports a URL origin because it accepts
	// endpoint profiles directly.
	probe.Address = address
	return probe
}

// ProbeOwnerAt asks the supplied URL origin who owns the Stem there. It never
// consults host or port environment variables.
func ProbeOwnerAt(ctx context.Context, endpoint string) OwnerProbe {
	clients := NewHTTPClients(http.DefaultTransport)
	return ProbeOwnerAtWithClient(ctx, endpoint, clients.Probe)
}

// ProbeOwnerAtWithClient probes readiness through the caller-selected client,
// allowing HTTPS readiness to use the same configured transport as mint and
// forwarding requests.
func ProbeOwnerAtWithClient(ctx context.Context, endpoint string, client *http.Client) OwnerProbe {
	endpoint = NormalizeEndpoint(endpoint)
	probe := OwnerProbe{Address: endpoint}
	if client == nil {
		probe.Err = fmt.Errorf("readiness HTTP client is not configured")
		return probe
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.Address+"/health", nil)
	if err != nil {
		probe.Err = err
		return probe
	}

	resp, err := client.Do(req)
	if err != nil {
		probe.Err = err
		return probe
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		probe.Err = fmt.Errorf("health endpoint returned %s", resp.Status)
		return probe
	}

	var report struct {
		Owner *int `json:"owner,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		probe.Err = err
		return probe
	}

	probe.Reached = true
	probe.Owner = report.Owner
	return probe
}
