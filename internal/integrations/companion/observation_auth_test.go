package companion

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type fakeObservationIdentityResolver struct {
	devices map[string]string
}

func (r fakeObservationIdentityResolver) ResolveObservationIdentity(_ context.Context, account, clientID string) (string, bool, error) {
	deviceID, found := r.devices[account+"/"+clientID]
	return deviceID, found, nil
}

func TestBearerObservationAuthenticator(t *testing.T) {
	tokenIndex := map[string]string{"first-secret": "nugget", "second-secret": "aimee"}
	identities := fakeObservationIdentityResolver{devices: map[string]string{
		"nugget/iphone-1": "dev_1",
		"aimee/iphone-2":  "dev_2",
	}}
	authenticator := NewBearerObservationAuthenticator(tokenIndex, identities.ResolveObservationIdentity)
	delete(tokenIndex, "first-secret")

	tests := []struct {
		name     string
		header   string
		clientID string
		want     ObservationPrincipal
		wantErr  error
	}{
		{name: "valid", header: "Bearer first-secret", clientID: " iphone-1 ", want: ObservationPrincipal{Account: "nugget", DeviceID: "dev_1"}},
		{name: "case insensitive scheme", header: "bearer second-secret", clientID: "iphone-2", want: ObservationPrincipal{Account: "aimee", DeviceID: "dev_2"}},
		{name: "wrong token", header: "Bearer wrong", clientID: "iphone-1", wantErr: ErrObservationUnauthorized},
		{name: "unknown device", header: "Bearer first-secret", clientID: "iphone-2", wantErr: ErrObservationUnauthorized},
		{name: "missing identity", header: "Bearer first-secret", wantErr: ErrObservationUnauthorized},
		{name: "surrounding token whitespace", header: "Bearer first-secret ", clientID: "iphone-1", wantErr: ErrObservationUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := ObservationAuthRequest{
				Header:          http.Header{"Authorization": []string{test.header}},
				ClaimedClientID: test.clientID,
			}
			got, err := authenticator.AuthenticateObservation(context.Background(), request)
			if got != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf("AuthenticateObservation() = (%+v, %v), want (%+v, %v)", got, err, test.want, test.wantErr)
			}
		})
	}
}
