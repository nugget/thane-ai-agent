package companion

import "testing"

func TestBearerObservationAuthenticator(t *testing.T) {
	tokenIndex := map[string]string{"first-secret": "nugget", "second-secret": "aimee"}
	authenticator := NewBearerObservationAuthenticator(tokenIndex)
	delete(tokenIndex, "first-secret")

	tests := []struct {
		name      string
		header    string
		identity  string
		want      ObservationPrincipal
		wantValid bool
	}{
		{name: "valid", header: "Bearer first-secret", identity: " iphone-1 ", want: ObservationPrincipal{Account: "nugget", DeviceIdentity: "iphone-1"}, wantValid: true},
		{name: "case insensitive scheme", header: "bearer second-secret", identity: "iphone-2", want: ObservationPrincipal{Account: "aimee", DeviceIdentity: "iphone-2"}, wantValid: true},
		{name: "wrong token", header: "Bearer wrong", identity: "iphone-1"},
		{name: "missing identity", header: "Bearer first-secret"},
		{name: "surrounding token whitespace", header: "Bearer first-secret ", identity: "iphone-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := authenticator.AuthenticateObservation(test.header, test.identity)
			if valid != test.wantValid || got != test.want {
				t.Fatalf("AuthenticateObservation() = (%+v, %t), want (%+v, %t)", got, valid, test.want, test.wantValid)
			}
		})
	}
}
