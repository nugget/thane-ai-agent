package api

import (
	"io"
	"net/http"
	"strings"
)

// captureBody reads and returns the body while allowing it to be read again
func captureBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return body, nil
}
