package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// The seed/grow capability family: grow a Seed — a bounded, well-specified
// intent — to Fruit. Where stoma.pass runs ONE command and sprout.grow
// runs an open-ended transcript, seed.grow hands the Stem a bounded unit of work
// — a goal plus a verification predicate plus explicit iteration and time bounds
// — and asks it to converge: build toward the goal, run the verify predicate,
// and iterate until the predicate passes or the bounds are spent. It is the
// "run + fix the failing tests" / "regenerate fixtures" shape.
//
// The Core owns only the contract and its validation. Execution — the sprout
// builder loop, the sealed-Terrarium verify run, worktree reconciliation — is
// injected as a transport-free port (WithSeed), so the Core never imports the
// conductor (see internal/core/boundary_test.go). Until that port is wired the
// capability reports that it is not wired rather than acting.
//
// Egress model (identical to stoma): the verify predicate and any build
// work run network-sealed; the only external reach is Stem-mediated and bounded
// by the delegation grant's egress allow-list. Egress carries json:"-", so it
// is set only by the Stem's own call sites from an authorized grant and can
// never be decoded from caller input — a caller structurally cannot widen it.

// Seed growth lifecycle statuses.
const (
	// SeedStatusRunning is the durable opening status of a Seed-owned
	// Phytomer. It is the only continuation-eligible lifecycle state.
	SeedStatusRunning = "running"
	// SeedStatusSettling is the non-terminal fence acquired after verification
	// passes and before successful Fruit may be persisted. It is not
	// continuation-eligible.
	SeedStatusSettling = "settling"
	// SeedStatusSatisfied means the verify predicate exited 0 within bounds.
	SeedStatusSatisfied = "satisfied"
	// SeedStatusExhausted means the iteration/time bounds were spent before
	// the verify predicate passed.
	SeedStatusExhausted = "exhausted"
	// SeedStatusWithered means the underlying sprout failed and was Abscised;
	// host state is untouched (the Terrarium contained it).
	SeedStatusWithered = "withered"
	// SeedStatusFruitPublicationFailed means Seed execution reached Fruit
	// publication, but no authoritative remote Fruit could be established.
	SeedStatusFruitPublicationFailed = "fruit-publication-failed"
	// SeedFailureCategoryFruitPublication is the safe diagnostic category for a
	// failed managed Fruit publication.
	SeedFailureCategoryFruitPublication = "fruit-publication"

	// Seed verification outcomes. These distinguish a completed predicate from
	// a timeout or an inability to execute the verifier. They are not Seed
	// terminal statuses.
	SeedVerificationOutcomePassed               = "passed"
	SeedVerificationOutcomePredicateFailed      = "predicate-failed"
	SeedVerificationOutcomeInfrastructureFailed = "infrastructure-failed"
)

// seedDefaultMaxIterations bounds the build/verify loop when the caller does
// not; seedMaximumMaxIterations caps what a caller may request. A Seed is a
// bounded intent — it is not an open-ended builder.
const (
	seedDefaultMaxIterations = 3
	seedMaximumMaxIterations = 10
	seedDefaultTimeout       = 15 * time.Minute
	seedMaximumTimeout       = time.Hour
)

// SeedGrowInput asks the Stem to grow a Seed: build toward Goal, then run
// Verify, iterating up to the bounds until Verify passes.
type SeedGrowInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Goal is the natural-language intent handed to the sprout builder — the
	// "what to accomplish" (e.g. "make the failing tests pass").
	Goal string `json:"goal"`
	// Verify is the argv command that defines "done": the Seed is satisfied
	// only when this command exits 0. It runs inside the sealed Terrarium, one
	// bounded command executed directly (never through a shell) — the same
	// harness stoma.pass uses. (The argv form is the predicate; a
	// named-sequence predicate is a compatible future addition.)
	Verify []string `json:"verify"`
	// MaxIterations bounds how many build/verify passes the loop may take. The
	// default applies when zero; a request above the maximum is capped.
	MaxIterations int `json:"maxIterations,omitempty"`
	// TimeoutSeconds bounds the whole growth's wall-clock; the default applies
	// when zero and a request above the maximum is capped.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Origin records which surface invoked the run (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
	// Detached asks Core to return the active Seed handle and Phytomer identity
	// after durable opening, and to grow in the background. Omitted or false
	// keeps the synchronous terminal SeedGrow behavior.
	Detached bool `json:"detached,omitempty"`
	// Egress is the authorized delegation grant's egress allow-list. It has no
	// JSON surface on purpose: only the Stem's own call sites populate it,
	// after the delegation authorizer has matched a grant, so no transport
	// input can ever widen egress (deny-all remains the default).
	Egress []string `json:"-"`
}

// SeedSpec is the fully resolved, transport-free Seed handed to the
// SeedOperations port.
type SeedSpec struct {
	Substrate     string
	Goal          string
	Verify        []string
	MaxIterations int
	Timeout       time.Duration
	Origin        string
	// Egress is the grant-supplied allow-list bounding Stem-mediated reach;
	// empty means deny-all.
	Egress []string
	// PhytomerID is the Stem-created canonical Phytomer for this Seed growth.
	PhytomerID string
}

// ErrSeedHistoryUnavailable is returned when async Seed ownership cannot be
// recorded because no persistence port is wired.
var ErrSeedHistoryUnavailable = errors.New("seed run history is not available")

// ErrSeedGrowthInvalid is returned when a caller presents a SeedGrowth that
// the Stem did not issue, or one whose bound identity was substituted.
var ErrSeedGrowthInvalid = errors.New("seed growth is not a prepared Stem lifecycle")

// SeedGrowth is an opaque Stem-owned lifecycle envelope for one Seed growth.
// Only PrepareSeed can issue a valid envelope. GrowPreparedSeed and
// OpenPreparedSeed refuse construction, mutation, or substitution.
type SeedGrowth struct {
	token      string
	phytomerID string
	pollen     string
	spec       SeedSpec
}

// PhytomerID is the Stem-created execution/observation identity bound to this
// growth. Empty on a zero envelope that was not issued by PrepareSeed.
func (g SeedGrowth) PhytomerID() string { return g.phytomerID }

// Substrate is the Substrate bound at preparation. Empty on a zero envelope.
func (g SeedGrowth) Substrate() string { return g.spec.Substrate }

// Pollen is the authenticated Pollen bound at preparation. Empty when the
// growth is not delegated.
func (g SeedGrowth) Pollen() string { return g.pollen }

// Goal is the prepared intent. Empty on a zero envelope.
func (g SeedGrowth) Goal() string { return g.spec.Goal }

// preparedSeed is the Stem-side record of one issued SeedGrowth.
type preparedSeed struct {
	phytomerID string
	pollen     string
	spec       SeedSpec
	handle     string
	startedAt  time.Time
	opened     bool
	opening    bool
}

// SeedDispatch is the Stem-composed async accept contract: the durable handle
// together with the canonical Phytomer identity. Adapters encode it; they do
// not assemble the relation.
type SeedDispatch struct {
	Handle     string `json:"handle"`
	PhytomerID string `json:"phytomerId"`
	Status     string `json:"status"`
}

// SeedOpening is the transport-free opening ownership record the Stem persists
// before an async dispatch is accepted.
type SeedOpening struct {
	Handle     string
	PhytomerID string
	Pollen     string
	Substrate  string
	Goal       string
	Status     string
	StartedAt  time.Time
}

// SeedSettlement is the transport-free terminal Seed record.
type SeedSettlement struct {
	Handle                  string
	PhytomerID              string
	Pollen                  string
	Substrate               string
	Goal                    string
	Status                  string
	Iterations              int
	Branch                  string
	Commit                  string
	Diff                    string
	Logs                    string
	Error                   string
	PublicationDiagnostic   *SeedPublicationDiagnostic
	VerificationDiagnostics []SeedVerificationDiagnostic
	StartedAt               time.Time
	FinishedAt              time.Time
}

// SeedPublicationDiagnostic is the Core-owned, credential-free explanation of
// a managed Fruit publication failure. It preserves the execution verdict so a
// publication problem cannot be mistaken for a failed Sprout.
type SeedPublicationDiagnostic struct {
	FailureCategory string `json:"failureCategory"`
	ExecutionStatus string `json:"executionStatus"`
	Phase           string `json:"phase"`
	Outcome         string `json:"outcome"`
	RetrySafe       bool   `json:"retrySafe"`
	Message         string `json:"message"`
	RequestID       string `json:"requestId,omitempty"`
}

// SeedVerificationDiagnostic is the Core-owned, credential-free explanation of
// one completed Seed verification iteration. It records only bounded Stem
// facts: whether the predicate passed, failed, timed out, or could not run.
type SeedVerificationDiagnostic struct {
	Iteration int    `json:"iteration"`
	Outcome   string `json:"outcome"`
	ExitCode  *int   `json:"exitCode,omitempty"`
	TimedOut  bool   `json:"timedOut"`
	Message   string `json:"message,omitempty"`
}

// SeedPersistence is the injected durable-ownership port. Core composes the
// Seed/Phytomer/Pollen/Substrate relation; the port only records it. Core
// never imports historydb.
type SeedPersistence struct {
	RecordOpening    func(ctx context.Context, opening SeedOpening) error
	RecordSettlement func(ctx context.Context, settled SeedSettlement) error
}

// WithSeedPersistence wires the durable Seed-ownership port.
func (s *Service) WithSeedPersistence(p SeedPersistence) *Service {
	s.seedPersist = p
	return s
}

// SeedGrowResult is the reviewable outcome of a grown Seed — the Fruit the
// Pollinator inspects. It is presented for review; nothing is merged.
type SeedGrowResult struct {
	// Status is satisfied, exhausted, withered, or fruit-publication-failed.
	Status string `json:"status"`
	// Iterations is how many build/verify passes ran.
	Iterations int `json:"iterations"`
	// PhytomerID is the Stem-created execution/observation identity for this
	// Seed growth. It is distinct from the async Seed handle.
	PhytomerID string `json:"phytomerId"`
	// Handle is the Core-minted durable Seed identity. Present only for
	// detached growth that has been durably opened; omitted on synchronous
	// terminal results.
	Handle string `json:"handle,omitempty"`
	// Branch is the reconciled branch the work landed on, for review.
	Branch string `json:"branch,omitempty"`
	// Commit is the independently identifiable Fruit commit SHA when Seed
	// execution actually produced one. Empty when the branch has no Seed change.
	Commit string `json:"commit,omitempty"`
	// Diff is the unified diff of the work, carried home for review (Phloem).
	Diff string `json:"diff,omitempty"`
	// Logs is the captured transcript/verify output (Xylem).
	Logs string `json:"logs,omitempty"`
	// PublicationDiagnostic is present only when managed Fruit publication
	// failed after Seed execution completed.
	PublicationDiagnostic *SeedPublicationDiagnostic `json:"publicationDiagnostic,omitempty"`
	// VerificationDiagnostics is one bounded diagnostic per completed
	// verification iteration. Absent when no verification ran.
	VerificationDiagnostics []SeedVerificationDiagnostic `json:"verificationDiagnostics,omitempty"`
}

// SeedOperations is the injection port for growing a Seed. Run may be nil, in
// which case the capability reports that it is not wired rather than acting
// until an execution port is provided.
type SeedOperations struct {
	// Run grows the Seed — build toward the goal, run the verify predicate in a
	// sealed Terrarium, iterate within bounds — and returns the reviewable
	// Fruit. Implementations own substrate resolution, the sprout lifecycle,
	// egress mediation, and worktree reconciliation.
	//
	// continuation is non-nil for every growth successfully opened through
	// OpenPreparedSeed. OpenPreparedSeed refuses if the per-run continuation
	// lifecycle is not wired. It is nil for ordinary synchronous SeedGrow;
	// that path must not create a durable opening or continuation ledger.
	Run func(ctx context.Context, spec SeedSpec, continuation *SeedContinuationLifecycle) (SeedGrowResult, error)
}

// WithSeed wires the Seed-growth execution port onto the Service and returns
// the Service for chaining.
func (s *Service) WithSeed(operations SeedOperations) *Service {
	s.seed = operations
	return s
}

// PrepareSeed validates the request and establishes exactly one canonical
// Phytomer for the Seed growth. Sync SeedGrow and async dispatch share this
// path so they cannot mint separate identities. It is not a governed command.
func (s *Service) PrepareSeed(ctx context.Context, in SeedGrowInput) (SeedGrowth, error) {
	spec, err := resolveSeedSpec(in)
	if err != nil {
		return SeedGrowth{}, err
	}
	// Mint before Phytomer creation so a crypto/rand failure cannot leave an
	// orphan session. An unused token after a later Initiate failure is never stored.
	token, err := s.mintPreparedSeedToken()
	if err != nil {
		return SeedGrowth{}, err
	}
	spec, err = s.bindSeedPhytomer(ctx, spec)
	if err != nil {
		return SeedGrowth{}, err
	}
	pollen := PollenFromContext(ctx)
	growth := SeedGrowth{
		token:      token,
		phytomerID: spec.PhytomerID,
		pollen:     pollen,
		spec:       spec,
	}
	s.seedMu.Lock()
	if s.preparedSeeds == nil {
		s.preparedSeeds = make(map[string]*preparedSeed)
	}
	s.preparedSeeds[token] = &preparedSeed{
		phytomerID: spec.PhytomerID,
		pollen:     pollen,
		spec:       spec,
	}
	s.seedMu.Unlock()
	return growth, nil
}

// GrowPreparedSeed executes a SeedGrowth issued by PrepareSeed. Substituting
// another Phytomer, Substrate, Pollen, or spec fails closed. It is not a
// governed command.
func (s *Service) GrowPreparedSeed(ctx context.Context, growth SeedGrowth) (SeedGrowResult, error) {
	if s.seed.Run == nil {
		return SeedGrowResult{}, fmt.Errorf("seed.grow is not wired: construct the Core with WithSeed(SeedOperations{Run: …})")
	}
	spec, pollen, handle, started, opened, err := s.takePreparedSeed(ctx, growth)
	if err != nil {
		return SeedGrowResult{}, err
	}
	var lifecycle *SeedContinuationLifecycle
	if opened {
		if err := s.openedContinuationLifecycleWired(); err != nil {
			return SeedGrowResult{}, err
		}
		lifecycle = s.newOpenedSeedContinuationLifecycle(ContinuationTarget{
			PhytomerID: spec.PhytomerID,
			Handle:     handle,
			Pollen:     pollen,
			Substrate:  spec.Substrate,
			Status:     SeedStatusRunning,
		})
		if lifecycle == nil {
			return SeedGrowResult{}, ErrContinuationNotWired
		}
	}
	result, err := s.seed.Run(ctx, spec, lifecycle)
	result.PhytomerID = spec.PhytomerID
	publicationFailed := err != nil && result.PublicationDiagnostic != nil && result.PublicationDiagnostic.FailureCategory == SeedFailureCategoryFruitPublication
	if publicationFailed {
		result.Status = SeedStatusFruitPublicationFailed
		result.Branch = ""
		result.Commit = ""
	}
	if opened {
		return s.finalizeOpenedSeed(ctx, lifecycle, spec, pollen, handle, started, result, err, publicationFailed)
	}
	return result, err
}

func (s *Service) finalizeOpenedSeed(ctx context.Context, lifecycle *SeedContinuationLifecycle, spec SeedSpec, pollen, handle string, started time.Time, result SeedGrowResult, runErr error, publicationFailed bool) (SeedGrowResult, error) {
	persistCtx, cancel := seedFinalizationContext(ctx)
	defer cancel()
	settled := composeOpenedSeedSettlement(spec, pollen, handle, started, result, runErr, publicationFailed)
	if runErr == nil && !publicationFailed && result.Status == SeedStatusSatisfied {
		if err := lifecycle.CompleteSuccessfulSettlement(persistCtx, settled); err != nil {
			result.Status = SeedStatusWithered
			return result, err
		}
		return result, nil
	}
	account, err := lifecycle.AccountTerminalFailure(persistCtx, settled)
	if err != nil {
		return result, err
	}
	if account.UnresolvedFailed > 0 {
		result.Status = SeedStatusWithered
		return result, ErrContinuationUndeliverable
	}
	return result, runErr
}

func composeOpenedSeedSettlement(spec SeedSpec, pollen, handle string, started time.Time, result SeedGrowResult, runErr error, publicationFailed bool) SeedSettlement {
	settled := SeedSettlement{
		Handle:                  handle,
		PhytomerID:              spec.PhytomerID,
		Pollen:                  pollen,
		Substrate:               spec.Substrate,
		Goal:                    spec.Goal,
		StartedAt:               started,
		FinishedAt:              time.Now().UTC(),
		Iterations:              result.Iterations,
		Diff:                    result.Diff,
		Logs:                    result.Logs,
		VerificationDiagnostics: CopySeedVerificationDiagnostics(result.VerificationDiagnostics),
	}
	if publicationFailed {
		settled.Status = SeedStatusFruitPublicationFailed
		settled.Branch = ""
		settled.Commit = ""
		copied := *result.PublicationDiagnostic
		settled.PublicationDiagnostic = &copied
		settled.Error = copied.Message
		return settled
	}
	if runErr != nil {
		settled.Status = SeedStatusWithered
		if errors.Is(runErr, ErrContinuationUndeliverable) {
			settled.Error = ErrContinuationUndeliverable.Error()
		} else {
			settled.Error = runErr.Error()
		}
		return settled
	}
	settled.Status = result.Status
	settled.Branch = result.Branch
	settled.Commit = result.Commit
	return settled
}

const seedFinalizationTimeout = 15 * time.Second

func seedFinalizationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, seedFinalizationTimeout)
}

// OpenPreparedSeed records durable Seed ownership from a Stem-issued growth
// before detached dispatch is accepted. An empty handle is minted by Core;
// Phytomer, Pollen, and Substrate come only from the envelope.
func (s *Service) OpenPreparedSeed(ctx context.Context, growth SeedGrowth, handle string) (SeedDispatch, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		minted, err := s.mintSeedHandle()
		if err != nil {
			return SeedDispatch{}, err
		}
		handle = minted
	}
	if err := s.openedContinuationLifecycleWired(); err != nil {
		return SeedDispatch{}, err
	}
	if s.seedPersist.RecordOpening == nil {
		return SeedDispatch{}, ErrSeedHistoryUnavailable
	}

	s.seedMu.Lock()
	rec, err := s.lookupPreparedSeedLocked(growth)
	if err != nil {
		s.seedMu.Unlock()
		return SeedDispatch{}, err
	}
	if rec.opened || rec.opening {
		s.seedMu.Unlock()
		return SeedDispatch{}, fmt.Errorf("%w: already opened", ErrSeedGrowthInvalid)
	}
	rec.opening = true
	phytomerID := rec.phytomerID
	opening := SeedOpening{
		Handle:     handle,
		PhytomerID: rec.phytomerID,
		Pollen:     rec.pollen,
		Substrate:  rec.spec.Substrate,
		Goal:       rec.spec.Goal,
		Status:     SeedStatusRunning,
		StartedAt:  time.Now().UTC(),
	}
	s.seedMu.Unlock()

	if err := s.seedPersist.RecordOpening(ctx, opening); err != nil {
		s.seedMu.Lock()
		rec.opening = false
		s.seedMu.Unlock()
		return SeedDispatch{}, err
	}

	s.seedMu.Lock()
	rec.handle = handle
	rec.startedAt = opening.StartedAt
	rec.opened = true
	rec.opening = false
	s.seedMu.Unlock()
	return SeedDispatch{
		Handle:     handle,
		PhytomerID: phytomerID,
		Status:     SeedStatusRunning,
	}, nil
}

// SeedGrow validates the request, prepares a canonical Phytomer, and grows
// that prepared envelope. Bounds are clamped to Stem-owned caps here, so a
// caller can only ever narrow — never widen — what a grant already permits.
func (s *Service) SeedGrow(ctx context.Context, in SeedGrowInput) (SeedGrowResult, error) {
	if s.seed.Run == nil {
		return SeedGrowResult{}, fmt.Errorf("seed.grow is not wired: construct the Core with WithSeed(SeedOperations{Run: …})")
	}
	growth, err := s.PrepareSeed(ctx, in)
	if err != nil {
		return SeedGrowResult{}, err
	}
	if !in.Detached {
		return s.GrowPreparedSeed(ctx, growth)
	}
	return s.startDetachedSeed(ctx, growth)
}

// startDetachedSeed durably opens a prepared growth, launches bounded
// background GrowPreparedSeed, and returns the active handle only after
// opening has been recorded. Request cancellation after that opening must
// not cancel the accepted growth.
func (s *Service) startDetachedSeed(ctx context.Context, growth SeedGrowth) (SeedGrowResult, error) {
	dispatch, err := s.OpenPreparedSeed(ctx, growth, "")
	if err != nil {
		return SeedGrowResult{}, err
	}
	bgCtx := context.Background()
	if ctx != nil {
		bgCtx = context.WithoutCancel(ctx)
	}
	go func() {
		_, _ = s.GrowPreparedSeed(bgCtx, growth)
	}()
	return SeedGrowResult{
		Handle:     dispatch.Handle,
		PhytomerID: dispatch.PhytomerID,
		Status:     SeedStatusRunning,
	}, nil
}

func resolveSeedSpec(in SeedGrowInput) (SeedSpec, error) {
	if strings.TrimSpace(in.Substrate) == "" {
		return SeedSpec{}, fmt.Errorf("substrate is required")
	}
	if strings.TrimSpace(in.Goal) == "" {
		return SeedSpec{}, fmt.Errorf("goal is required (the intent handed to the builder)")
	}
	// Argument tokens pass through verbatim (a token may legitimately carry
	// whitespace); only the executable token must be non-blank.
	verify := append([]string(nil), in.Verify...)
	if len(verify) == 0 || strings.TrimSpace(verify[0]) == "" {
		return SeedSpec{}, fmt.Errorf("verify is required (an argv vector whose exit-0 defines success)")
	}
	if in.MaxIterations < 0 {
		return SeedSpec{}, fmt.Errorf("maxIterations must not be negative")
	}
	if in.TimeoutSeconds < 0 {
		return SeedSpec{}, fmt.Errorf("timeoutSeconds must not be negative")
	}

	maxIterations := seedDefaultMaxIterations
	if in.MaxIterations > 0 {
		maxIterations = in.MaxIterations
	}
	if maxIterations > seedMaximumMaxIterations {
		maxIterations = seedMaximumMaxIterations
	}

	timeout := seedDefaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	if timeout > seedMaximumTimeout {
		timeout = seedMaximumTimeout
	}

	return SeedSpec{
		Substrate:     strings.TrimSpace(in.Substrate),
		Goal:          strings.TrimSpace(in.Goal),
		Verify:        verify,
		MaxIterations: maxIterations,
		Timeout:       timeout,
		Origin:        in.Origin,
		Egress:        append([]string(nil), in.Egress...),
	}, nil
}

func (s *Service) bindSeedPhytomer(ctx context.Context, spec SeedSpec) (SeedSpec, error) {
	if s.sessions == nil {
		return SeedSpec{}, fmt.Errorf("seed.grow requires a phytomer session manager")
	}
	sess, err := s.sessions.Initiate(ctx, spec.Origin, session.Preferences{Substrate: spec.Substrate})
	if err != nil {
		return SeedSpec{}, fmt.Errorf("establish seed phytomer: %w", err)
	}
	spec.PhytomerID = sess.ID
	return spec, nil
}

func (s *Service) mintPreparedSeedToken() (string, error) {
	if s != nil && s.newPreparedSeedToken != nil {
		return s.newPreparedSeedToken()
	}
	return generatePreparedSeedToken()
}

func (s *Service) mintSeedHandle() (string, error) {
	if s != nil && s.newSeedHandle != nil {
		return s.newSeedHandle()
	}
	return generateSeedHandle()
}

func generateSeedHandle() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint seed handle: %w", err)
	}
	return "seed-" + hex.EncodeToString(buf), nil
}

func generatePreparedSeedToken() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("prepare seed: mint lifecycle token: %w", err)
	}
	return "seed-growth-" + hex.EncodeToString(buf), nil
}

func (s *Service) lookupPreparedSeed(growth SeedGrowth) (*preparedSeed, error) {
	if s == nil {
		return nil, ErrSeedGrowthInvalid
	}
	s.seedMu.Lock()
	defer s.seedMu.Unlock()
	return s.lookupPreparedSeedLocked(growth)
}

func (s *Service) lookupPreparedSeedLocked(growth SeedGrowth) (*preparedSeed, error) {
	if strings.TrimSpace(growth.token) == "" {
		return nil, ErrSeedGrowthInvalid
	}
	rec, ok := s.preparedSeeds[growth.token]
	if !ok || rec == nil {
		return nil, ErrSeedGrowthInvalid
	}
	if err := preparedSeedMatches(growth, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *Service) takePreparedSeed(ctx context.Context, growth SeedGrowth) (spec SeedSpec, pollen, handle string, started time.Time, opened bool, err error) {
	if s == nil {
		return SeedSpec{}, "", "", time.Time{}, false, ErrSeedGrowthInvalid
	}
	s.seedMu.Lock()
	defer s.seedMu.Unlock()
	rec, err := s.lookupPreparedSeedLocked(growth)
	if err != nil {
		return SeedSpec{}, "", "", time.Time{}, false, err
	}
	if rec.opening {
		return SeedSpec{}, "", "", time.Time{}, false, fmt.Errorf("%w: durable opening is not complete", ErrSeedGrowthInvalid)
	}
	if PollenFromContext(ctx) != rec.pollen {
		return SeedSpec{}, "", "", time.Time{}, false, fmt.Errorf("%w: pollen does not match the prepared growth", ErrSeedGrowthInvalid)
	}
	spec = rec.spec
	pollen = rec.pollen
	handle = rec.handle
	started = rec.startedAt
	opened = rec.opened
	delete(s.preparedSeeds, growth.token)
	return spec, pollen, handle, started, opened, nil
}

func preparedSeedMatches(growth SeedGrowth, rec *preparedSeed) error {
	if rec == nil {
		return ErrSeedGrowthInvalid
	}
	if growth.phytomerID != rec.phytomerID || rec.spec.PhytomerID != rec.phytomerID || growth.spec.PhytomerID != rec.phytomerID {
		return fmt.Errorf("%w: phytomer substitution is refused", ErrSeedGrowthInvalid)
	}
	if growth.pollen != rec.pollen {
		return fmt.Errorf("%w: pollen substitution is refused", ErrSeedGrowthInvalid)
	}
	if !seedSpecsEqual(growth.spec, rec.spec) {
		return fmt.Errorf("%w: seed substitution is refused", ErrSeedGrowthInvalid)
	}
	return nil
}

func seedSpecsEqual(a, b SeedSpec) bool {
	return a.Substrate == b.Substrate &&
		a.Goal == b.Goal &&
		a.MaxIterations == b.MaxIterations &&
		a.Timeout == b.Timeout &&
		a.Origin == b.Origin &&
		a.PhytomerID == b.PhytomerID &&
		stringSlicesEqual(a.Verify, b.Verify) &&
		stringSlicesEqual(a.Egress, b.Egress)
}

// CopySeedVerificationDiagnostics copies a verification diagnostic slice,
// including exit-code pointers, so persistence and observation cannot share
// mutable backing with the execution result.
func CopySeedVerificationDiagnostics(src []SeedVerificationDiagnostic) []SeedVerificationDiagnostic {
	if len(src) == 0 {
		return nil
	}
	out := make([]SeedVerificationDiagnostic, len(src))
	copy(out, src)
	for i := range src {
		if src[i].ExitCode != nil {
			code := *src[i].ExitCode
			out[i].ExitCode = &code
		}
	}
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// seedCapabilities declares the seed family's registry entry, bound to this
// Service's typed method — identical in shape to the stoma family. The
// egress allow-list deliberately has no place in this schema: it is grant
// material, supplied only by the Stem's own call sites.
func (s *Service) seedCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapSeedGrow,
			Description: "Grow a Seed: build toward a goal and iterate until a verify command exits 0, within iteration/time bounds, inside a network-sealed terrarium (external reach only via a delegation grant's egress allow-list). Returns the Fruit for review; nothing is merged.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"goal":      stringProp("The intent handed to the builder — what the Seed must accomplish."),
				"verify": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "The argv vector whose exit-0 defines success; run directly (never through a shell) inside the sealed terrarium.",
				},
				"maxIterations":  map[string]any{"type": "integer", "description": "Maximum build/verify passes (default 3, maximum 10)."},
				"timeoutSeconds": map[string]any{"type": "integer", "description": "Whole-growth wall-clock bound in seconds (default 900, maximum 3600)."},
				"origin":         stringProp("Interaction origin recorded on the run (cli, mcp, rest)."),
				"detached":       map[string]any{"type": "boolean", "description": "When true, return the active handle and Phytomer identity after durable opening and grow in the background. Default false: block until the Seed is terminal."},
			}, []string{"substrate", "goal", "verify"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in SeedGrowInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.SeedGrow(ctx, in)
			},
		},
	}
}
