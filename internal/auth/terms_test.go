package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestRegisterStoresConsent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	before := time.Now().Add(-time.Second)
	ts, err := e.svc.Register(ctx, e.client("10.0.0.5"), "terms_given", "", "correct horse", nil,
		Consent{TermsVersion: ptr(" 2026-11-19 "), AgeConfirmed: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.store.Users().Get(ctx, ts.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.TermsVersion == nil || *u.TermsVersion != "2026-11-19" {
		t.Fatalf("terms version: %v", u.TermsVersion)
	}
	if u.TermsAcceptedAt == nil || u.TermsAcceptedAt.Before(before) || u.AgeConfirmedAt == nil || u.AgeConfirmedAt.Before(before) {
		t.Fatalf("acceptance times: %v %v", u.TermsAcceptedAt, u.AgeConfirmedAt)
	}

	ts, err = e.svc.Register(ctx, e.client("10.0.0.5"), "terms_missing", "", "correct horse", nil, Consent{})
	if err != nil {
		t.Fatalf("without consent while not required: %v", err)
	}
	if u, err = e.store.Users().Get(ctx, ts.User.ID); err != nil || u.TermsVersion != nil || u.TermsAcceptedAt != nil || u.AgeConfirmedAt != nil {
		t.Fatalf("nothing is recorded without consent: %v %+v", err, u)
	}
}

func TestRegisterRejectsInvalidConsent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		consent Consent
		field   string
	}{
		"age not confirmed":  {Consent{TermsVersion: ptr("2026-11-19"), AgeConfirmed: ptr(false)}, "age_confirmed"},
		"version not a date": {Consent{TermsVersion: ptr("latest"), AgeConfirmed: ptr(true)}, "terms_version"},
		"impossible date":    {Consent{TermsVersion: ptr("2026-02-30")}, "terms_version"},
		"empty version":      {Consent{TermsVersion: ptr(" ")}, "terms_version"},
	} {
		var ve *ValidationError
		if _, err := e.svc.Register(ctx, e.client("10.0.0.6"), "invalid_consent", "", "correct horse", nil, tc.consent); !errors.As(err, &ve) || ve.Field != tc.field {
			t.Fatalf("%s: got %v, want a validation error on %s", name, err, tc.field)
		}
	}
	if _, err := e.store.Users().GetByUsername(ctx, "invalid_consent"); err == nil {
		t.Fatal("a refused registration must not create the account")
	}
}

func TestRegisterRequiresConsentWhenConfigured(t *testing.T) {
	e := newEnv(t)
	e.svc.requireTerms = true
	ctx := context.Background()
	for name, tc := range map[string]struct {
		consent Consent
		field   string
	}{
		"nothing":       {Consent{}, "terms_version"},
		"only terms":    {Consent{TermsVersion: ptr("2026-11-19")}, "age_confirmed"},
		"only age":      {Consent{AgeConfirmed: ptr(true)}, "terms_version"},
		"age not given": {Consent{TermsVersion: ptr("2026-11-19"), AgeConfirmed: ptr(false)}, "age_confirmed"},
	} {
		var ve *ValidationError
		if _, err := e.svc.Register(ctx, e.client("10.0.0.7"), "required_consent", "", "correct horse", nil, tc.consent); !errors.As(err, &ve) || ve.Field != tc.field {
			t.Fatalf("%s: got %v, want a validation error on %s", name, err, tc.field)
		}
	}
	if _, err := e.svc.Register(ctx, e.client("10.0.0.7"), "required_consent", "", "correct horse", nil,
		Consent{TermsVersion: ptr("2026-11-19"), AgeConfirmed: ptr(true)}); err != nil {
		t.Fatalf("full consent: %v", err)
	}
}
