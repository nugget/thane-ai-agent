package listen

import "net/http"

// RouteRegistrar is the subset of *http.ServeMux that route-mounting
// packages need. Accepting the interface rather than the concrete mux
// lets the native API hand every registrar a recording table, so no route
// can reach the mux without being seen by the posture test that decides
// whether it is gated or public.
type RouteRegistrar interface {
	Handle(pattern string, handler http.Handler)
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}
