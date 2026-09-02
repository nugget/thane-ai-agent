package api

// Request body caps per surface. The shared listener bounds live in
// package listen; these differ by surface because payload shapes differ.
const (
	// nativeMaxBodyBytes caps native /v1 request bodies. The largest
	// legitimate payloads are loop definitions and contact records; 8 MiB
	// is far above either while bounding what an unauthenticated caller
	// can make the decoder hold.
	nativeMaxBodyBytes = 8 << 20
	// compatMaxBodyBytes caps compat-shim bodies, which may carry chat
	// history plus base64 images. Matches the Ollama handler's own cap.
	compatMaxBodyBytes = 32 << 20
)
