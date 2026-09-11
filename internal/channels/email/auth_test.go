package email

import "testing"

// TestAuthenticationNormalizeEnforcesVerifiedInvariant pins the rule
// that Verified is true only for a verified status from a method that
// binds a message to a person. A domain-level method or a bare claim
// cannot smuggle a verified flag through.
func TestAuthenticationNormalizeEnforcesVerifiedInvariant(t *testing.T) {
	tests := []struct {
		name     string
		in       Authentication
		verified bool
		status   AuthStatus
		method   AuthMethod
	}{
		{name: "zero value is absent", in: Authentication{}, status: AuthAbsent, method: AuthMethodNone},
		{name: "pgp verified stays verified", in: Authentication{Method: AuthMethodPGP, Status: AuthVerified}, verified: true, status: AuthVerified, method: AuthMethodPGP},
		{name: "smime verified stays verified", in: Authentication{Method: AuthMethodSMIME, Status: AuthVerified, Verified: false}, verified: true, status: AuthVerified, method: AuthMethodSMIME},
		{name: "dkim cannot verify a person", in: Authentication{Method: AuthMethodDKIM, Status: AuthVerified, Verified: true}, status: AuthFailed, method: AuthMethodDKIM},
		{name: "none cannot verify", in: Authentication{Method: AuthMethodNone, Status: AuthVerified, Verified: true}, status: AuthFailed, method: AuthMethodNone},
		{name: "verified flag without status is dropped", in: Authentication{Method: AuthMethodPGP, Status: AuthUnavailable, Verified: true}, status: AuthUnavailable, method: AuthMethodPGP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.normalize()
			if got.Verified != tt.verified || got.Status != tt.status || got.Method != tt.method {
				t.Errorf("normalize(%+v) = %+v", tt.in, got)
			}
			if got.Status == AuthFailed && tt.in.Status == AuthVerified && got.Reason == "" {
				t.Error("a demoted claim must say why")
			}
		})
	}

	absent := AbsentAuthentication()
	if absent.Method != AuthMethodNone || absent.Status != AuthAbsent || absent.Verified {
		t.Errorf("AbsentAuthentication() = %+v", absent)
	}
}
