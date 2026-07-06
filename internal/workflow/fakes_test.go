package workflow

import (
	"context"
	"sync"

	"onboarding-service/internal/platform/auth0"
	"onboarding-service/internal/platform/provisioning"
	"onboarding-service/internal/repo"
)

// stubJourneyRepo returns a preset journey for FindByUserID and counts upserts.
type stubJourneyRepo struct {
	doc     *repo.OnboardingJourneyDoc
	upserts int
}

func (s *stubJourneyRepo) FindByUserID(context.Context, string) (*repo.OnboardingJourneyDoc, error) {
	return s.doc, nil
}

func (s *stubJourneyRepo) Upsert(_ context.Context, doc *repo.OnboardingJourneyDoc) error {
	s.upserts++
	s.doc = doc
	return nil
}

func (s *stubJourneyRepo) SetOrgID(_ context.Context, userID, orgID string) error {
	if s.doc == nil {
		s.doc = &repo.OnboardingJourneyDoc{UserID: userID}
	}
	s.doc.OrgID = orgID
	return nil
}

// fakeProvisioningRepo is an in-memory ProvisioningRecordRepo with copy-in/out
// semantics so callers can't alias the stored map.
type fakeProvisioningRepo struct {
	mu   sync.Mutex
	recs map[string]*repo.ProvisioningRecordDoc
}

func newFakeProvisioningRepo() *fakeProvisioningRepo {
	return &fakeProvisioningRepo{recs: map[string]*repo.ProvisioningRecordDoc{}}
}

func (f *fakeProvisioningRepo) GetByOrgID(_ context.Context, orgID string) (*repo.ProvisioningRecordDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.recs[orgID]
	if !ok {
		return nil, nil
	}
	return cloneRecord(r), nil
}

func (f *fakeProvisioningRepo) Upsert(_ context.Context, doc *repo.ProvisioningRecordDoc) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs[doc.OrgID] = cloneRecord(doc)
	return nil
}

func cloneRecord(r *repo.ProvisioningRecordDoc) *repo.ProvisioningRecordDoc {
	cp := *r
	cp.Resources = make(map[string]string, len(r.Resources))
	for k, v := range r.Resources {
		cp.Resources[k] = v
	}
	return &cp
}

// countingOrgCreator records how many times the Auth0 org creation runs.
type countingOrgCreator struct{ calls int }

func (c *countingOrgCreator) CreateOrganisation(_ context.Context, in auth0.CreateOrgInput) (string, error) {
	c.calls++
	return "org_" + in.UserID, nil
}
func (c *countingOrgCreator) UserEmail(_ context.Context, userID string) (string, error) {
	return userID + "@example.com", nil
}

// countingProvisioner records how many times each provisioning action runs.
type countingProvisioner struct{ kong, svix, lago, aws int }

func (c *countingProvisioner) Kong(_ context.Context, in provisioning.ProvisionInput) (string, error) {
	c.kong++
	return "kong_" + in.OrgID, nil
}
func (c *countingProvisioner) Svix(_ context.Context, in provisioning.ProvisionInput) (string, error) {
	c.svix++
	return "svix_" + in.OrgID, nil
}
func (c *countingProvisioner) Lago(_ context.Context, in provisioning.ProvisionInput) (string, error) {
	c.lago++
	return "lago_" + in.OrgID, nil
}
func (c *countingProvisioner) AWS(_ context.Context, in provisioning.ProvisionInput) (string, error) {
	c.aws++
	return "aws_" + in.OrgID, nil
}
