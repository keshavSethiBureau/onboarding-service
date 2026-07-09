package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"onboarding-service/internal/auth"
	"onboarding-service/internal/config"
	"onboarding-service/internal/service/dto"
	"onboarding-service/internal/workflow"
)

// fakeBackend is a shared in-memory stand-in for the workflow + read-model. It
// models the two invariants under test: (1) one workflow per user — Start
// "creates" a journey at most once per userId even under concurrency; (2) the
// EMAIL_VERIFIED signal advances past the recorded first step. GetState reads the
// same store, standing in for the PersistJourneyState-maintained read-model.
type fakeBackend struct {
	mu            sync.Mutex
	creates       map[string]int  // userId -> number of times Start actually created
	started       map[string]bool // userId -> workflow exists
	emailVerified map[string]bool // userId -> EMAIL_VERIFIED signalled
	signalCalls   map[string]int  // userId -> SignalEmailVerified call count

	// AdvanceStep support: a test may seed an exact journey read-model per user;
	// SignalStep records the steps it was asked to advance.
	seeded      map[string]*dto.OnboardingJourney
	stepSignals map[string][]string // userId -> step names passed to SignalStep
	signalErr   error               // forces SignalStep to fail when set
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		creates:       map[string]int{},
		started:       map[string]bool{},
		emailVerified: map[string]bool{},
		signalCalls:   map[string]int{},
		seeded:        map[string]*dto.OnboardingJourney{},
		stepSignals:   map[string][]string{},
	}
}

// seed installs an exact journey read-model for a user (AdvanceStep tests).
func (f *fakeBackend) seed(userID string, j *dto.OnboardingJourney) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seeded[userID] = j
}

func (f *fakeBackend) Start(_ context.Context, userID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started[userID] { // USE_EXISTING semantics: create once, else no-op
		f.started[userID] = true
		f.creates[userID]++
	}
	return "run-" + userID, nil
}

func (f *fakeBackend) SignalEmailVerified(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signalCalls[userID]++
	f.emailVerified[userID] = true // no-op if already set
	return nil
}

func (f *fakeBackend) SignalStep(_ context.Context, userID, stepName string, _ workflow.SignalPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stepSignals[userID] = append(f.stepSignals[userID], stepName)
	return f.signalErr
}

func (f *fakeBackend) stepSignalCount(userID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stepSignals[userID])
}

// GetState returns a seeded journey when present (AdvanceStep tests); otherwise
// it mirrors the executor: a freshly created journey rests on USER_SIGNED_UP, and
// once EMAIL_VERIFIED is signalled the journey advances.
func (f *fakeBackend) GetState(_ context.Context, userID string) (*dto.OnboardingJourney, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j, ok := f.seeded[userID]; ok {
		return j, nil
	}
	step := workflow.StepUserSignedUp
	if f.emailVerified[userID] {
		step = workflow.StepEmailVerified
	}
	return &dto.OnboardingJourney{UserID: userID, CurrentStep: step, Status: dto.StatusInProgress}, nil
}

// newTestRouter builds a gin engine with the REAL auth middleware in dev mode
// (identity from X-User-Id / X-Email-Verified headers) and the onboarding routes,
// wired with a validator registry whose vertical cache knows "KYC".
func newTestRouter(t *testing.T, be *fakeBackend) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mw, err := auth.New(auth.Config{Enabled: false})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	vc := config.NewVerticalCache()
	vc.Replace([]config.Vertical{{Name: "KYC"}}, nil)
	r := gin.New()
	ctrl := NewOnboardingController(be, be, NewValidatorRegistry(vc))
	ctrl.RegisterRoutes(r, mw.Handler())
	return r
}

// do fires a request with the given dev-identity headers and returns the recorder.
func do(r *gin.Engine, method, path, userID, emailVerified string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
	}
	if emailVerified != "" {
		req.Header.Set("X-Email-Verified", emailVerified)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeState(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return body
}

//  1. First /state call for a new user creates the journey and records
//     USER_SIGNED_UP; the returned state reflects it.
func TestState_FirstCall_CreatesJourneyAtUserSignedUp(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)

	w := do(r, http.MethodGet, "/v1/onboarding/state", "user1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if be.creates["user1"] != 1 {
		t.Errorf("workflow creates = %d, want 1", be.creates["user1"])
	}
	if got := decodeState(t, w)["current_step"]; got != workflow.StepUserSignedUp {
		t.Errorf("current_step = %v, want USER_SIGNED_UP (entry step recorded)", got)
	}
}

// 2. A verified-email token advances EMAIL_VERIFIED; an unverified one does not.
func TestState_EmailVerifiedAdvances(t *testing.T) {
	for _, tc := range []struct {
		name        string
		verifiedHdr string
		wantSignal  int
		wantStep    string
	}{
		{"verified advances", "true", 1, workflow.StepEmailVerified},
		{"unverified does not", "", 0, workflow.StepUserSignedUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be := newFakeBackend()
			r := newTestRouter(t, be)

			w := do(r, http.MethodGet, "/v1/onboarding/state", "user1", tc.verifiedHdr)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if be.signalCalls["user1"] != tc.wantSignal {
				t.Errorf("SignalEmailVerified calls = %d, want %d", be.signalCalls["user1"], tc.wantSignal)
			}
			if got := decodeState(t, w)["current_step"]; got != tc.wantStep {
				t.Errorf("current_step = %v, want %s", got, tc.wantStep)
			}
		})
	}
}

// 3. Repeated calls are no-ops: no duplicate journey, state unchanged.
func TestState_RepeatedCallsNoOp(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)

	var last string
	for i := 0; i < 5; i++ {
		w := do(r, http.MethodGet, "/v1/onboarding/state", "user1", "true")
		if w.Code != http.StatusOK {
			t.Fatalf("call %d status = %d", i, w.Code)
		}
		step, _ := decodeState(t, w)["current_step"].(string)
		if last != "" && step != last {
			t.Errorf("state changed across idempotent calls: %q -> %q", last, step)
		}
		last = step
	}
	if be.creates["user1"] != 1 {
		t.Errorf("workflow creates = %d after 5 calls, want 1 (idempotent)", be.creates["user1"])
	}
}

// 4. Two (many) concurrent first calls for the same user yield exactly one journey.
func TestState_ConcurrentSingleWorkflow(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)

	const n = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			do(r, http.MethodGet, "/v1/onboarding/state", "user1", "true")
		}()
	}
	close(start)
	wg.Wait()

	if be.creates["user1"] != 1 {
		t.Fatalf("workflow creates under %d concurrent calls = %d, want exactly 1", n, be.creates["user1"])
	}
}

// 5. An invalid/missing token is rejected with 401 BEFORE any workflow call.
func TestState_Unauthenticated401BeforeWorkflow(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)

	w := do(r, http.MethodGet, "/v1/onboarding/state", "", "") // no X-User-Id -> middleware 401
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(be.creates) != 0 {
		t.Errorf("workflow was started for an unauthenticated caller: %v", be.creates)
	}
}

// ---- Generic step-advance endpoint (POST /v1/onboarding/steps/:step_name) ----

// doStep POSTs a step body with dev identity headers.
func doStep(r *gin.Engine, userID, step, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/onboarding/steps/"+step, strings.NewReader(body))
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// seedAt seeds a user's journey at currentStep on catalog v1 with the given
// completed steps.
func seedAt(be *fakeBackend, userID, currentStep, status string, completed ...string) {
	steps := make([]dto.StepSummary, len(completed))
	for i, s := range completed {
		steps[i] = dto.StepSummary{StepName: s, Status: dto.StatusCompleted}
	}
	be.seed(userID, &dto.OnboardingJourney{
		UserID: userID, CurrentStep: currentStep, Status: status,
		StepCatalogVersion: workflow.CatalogVersion, Steps: steps,
	})
}

// Advancing the current step with valid input signals the step and returns state.
func TestAdvanceStep_CurrentStepValidInput(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)
	seedAt(be, "user1", workflow.StepOrganisationCreated, dto.StatusInProgress,
		workflow.StepUserSignedUp, workflow.StepEmailVerified)

	w := doStep(r, "user1", workflow.StepOrganisationCreated,
		`{"input":{"display_name":"Acme","tnc_accepted":"true"}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if be.stepSignalCount("user1") != 1 {
		t.Errorf("SignalStep calls = %d, want 1", be.stepSignalCount("user1"))
	}
}

// Out-of-order step (in the catalog but not current) is rejected 409, not signalled.
func TestAdvanceStep_OutOfOrder409(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)
	seedAt(be, "user1", workflow.StepOrganisationCreated, dto.StatusInProgress)

	w := doStep(r, "user1", workflow.StepVerticalSelected, `{"input":{"vertical_name":"KYC"}}`)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if be.stepSignalCount("user1") != 0 {
		t.Errorf("out-of-order step was signalled")
	}
}

// A step not in the pinned catalog is rejected 409.
func TestAdvanceStep_NotInCatalog409(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)
	seedAt(be, "user1", workflow.StepOrganisationCreated, dto.StatusInProgress)

	w := doStep(r, "user1", "NOT_A_STEP", `{"input":{}}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if be.stepSignalCount("user1") != 0 {
		t.Errorf("unknown step was signalled")
	}
}

// A validator step with missing/invalid input is rejected 400, not signalled.
// Also proves org-creation validation survived the move into the registry.
func TestAdvanceStep_InvalidInput400(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing both", `{"input":{}}`},
		{"missing tnc_accepted", `{"input":{"display_name":"Acme"}}`},
		{"missing display_name", `{"input":{"tnc_accepted":"true"}}`},
		{"empty body", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be := newFakeBackend()
			r := newTestRouter(t, be)
			seedAt(be, "user1", workflow.StepOrganisationCreated, dto.StatusInProgress)

			w := doStep(r, "user1", workflow.StepOrganisationCreated, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
			if be.stepSignalCount("user1") != 0 {
				t.Errorf("invalid input was signalled")
			}
		})
	}
}

// VERTICAL_SELECTED validator rejects an unknown vertical (400) and accepts a known one.
func TestAdvanceStep_VerticalValidator(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)
	seedAt(be, "user1", workflow.StepVerticalSelected, dto.StatusInProgress)

	if w := doStep(r, "user1", workflow.StepVerticalSelected, `{"input":{"vertical_name":"NOPE"}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown vertical status = %d, want 400", w.Code)
	}
	if w := doStep(r, "user1", workflow.StepVerticalSelected, `{"input":{"vertical_name":"KYC"}}`); w.Code != http.StatusOK {
		t.Fatalf("known vertical status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
}

// Re-submitting an already-completed step is a no-op returning state (not signalled).
func TestAdvanceStep_CompletedStepNoOp(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)
	seedAt(be, "user1", workflow.StepVerticalSelected, dto.StatusInProgress,
		workflow.StepUserSignedUp, workflow.StepEmailVerified, workflow.StepOrganisationCreated)

	w := doStep(r, "user1", workflow.StepOrganisationCreated,
		`{"input":{"display_name":"Acme","tnc_accepted":"true"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no-op)", w.Code)
	}
	if be.stepSignalCount("user1") != 0 {
		t.Errorf("completed step was re-signalled")
	}
}

// A completed journey just returns state for any step.
func TestAdvanceStep_CompletedJourneyReturnsState(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)
	seedAt(be, "user1", workflow.StepResourcesProvisioned, dto.StatusCompleted)

	w := doStep(r, "user1", workflow.StepOnboardingCompleted, `{"input":{}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := decodeState(t, w)["status"]; got != dto.StatusCompleted {
		t.Errorf("status = %v, want completed", got)
	}
	if be.stepSignalCount("user1") != 0 {
		t.Errorf("completed journey was signalled")
	}
}

// Unauthenticated advance is rejected 401 before any signal.
func TestAdvanceStep_Unauthenticated401(t *testing.T) {
	be := newFakeBackend()
	r := newTestRouter(t, be)
	req := httptest.NewRequest(http.MethodPost, "/v1/onboarding/steps/"+workflow.StepOrganisationCreated, strings.NewReader(`{"input":{}}`))
	req.Header.Set("Content-Type", "application/json") // no X-User-Id
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if be.stepSignalCount("user1") != 0 {
		t.Errorf("signalled for an unauthenticated caller")
	}
}

// REMOVED(single-entry): signup + Auth /me tests. There is no signup endpoint and
// this service never calls the Auth Service, so these no longer apply. Retained
// commented per the removal convention.
/*
func TestSignup_MeUnavailable_RetryableNoJourney(t *testing.T) {
	be := newFakeBackend()
	me := &fakeMe{err: authsvc.ErrAuthUnavailable}
	r := newTestRouter(t, be, me)
	w := do(r, http.MethodPost, "/v1/onboarding/signup", "user1", "true")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (retryable)", w.Code)
	}
	if body := decodeState(t, w); body["retryable"] != true {
		t.Errorf("response missing retryable=true: %v", body)
	}
	if len(be.creates) != 0 || len(be.started) != 0 {
		t.Errorf("a partial journey was started despite /me failure: creates=%v started=%v", be.creates, be.started)
	}
}

func TestSignup_MeRejected_401NoJourney(t *testing.T) {
	be := newFakeBackend()
	me := &fakeMe{err: errors.New("auth /me rejected the request (status 403)")}
	r := newTestRouter(t, be, me)
	w := do(r, http.MethodPost, "/v1/onboarding/signup", "user1", "true")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(be.creates) != 0 {
		t.Errorf("a journey was started despite /me rejection: %v", be.creates)
	}
}
*/
