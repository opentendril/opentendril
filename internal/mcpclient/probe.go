package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
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
}

// ProbeOwner asks the resolved address who owns the Stem there.
//
// Shared by mode selection and by hardiness so the two cannot drift into
// disagreeing about whether another principal is serving on this host.
func ProbeOwner(ctx context.Context) OwnerProbe {
	probe := OwnerProbe{Address: ResolveStemAddress("")}

	client := &http.Client{
		Timeout: 2 * time.Second, // probe carries its own 2-second bound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+probe.Address+"/health", nil)
	if err != nil {
		return probe
	}

	resp, err := client.Do(req)
	if err != nil {
		return probe
	}
	defer resp.Body.Close()

	var report struct {
		Owner *int `json:"owner,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return probe
	}

	probe.Reached = true
	probe.Owner = report.Owner
	return probe
}
