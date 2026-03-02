package service

import (
	"context"
	"testing"
)

func TestWithHTTPUpstreamProfile_DefaultKeepsContext(t *testing.T) {
	ctx := context.Background()
	got := WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileDefault)
	if got != ctx {
		t.Fatalf("expected default profile to keep original context")
	}
}

func TestWithHTTPUpstreamProfile_TODOContextSetsProfile(t *testing.T) {
	ctx := WithHTTPUpstreamProfile(context.TODO(), HTTPUpstreamProfileOpenAI)
	if ctx == nil {
		t.Fatalf("expected non-nil context")
	}
	if profile := HTTPUpstreamProfileFromContext(ctx); profile != HTTPUpstreamProfileOpenAI {
		t.Fatalf("expected profile %q, got %q", HTTPUpstreamProfileOpenAI, profile)
	}
}

func TestHTTPUpstreamProfileFromContext_UnknownValueFallsBackDefault(t *testing.T) {
	type badKey struct{}
	ctx := context.WithValue(context.Background(), httpUpstreamProfileContextKey{}, HTTPUpstreamProfile("unknown"))
	ctx = context.WithValue(ctx, badKey{}, "x")
	if profile := HTTPUpstreamProfileFromContext(ctx); profile != HTTPUpstreamProfileDefault {
		t.Fatalf("expected default profile, got %q", profile)
	}
}
