// Bootstraps the Scalar API reference against the embedded specs.
//
// A separate file rather than an inline block so /docs keeps
// script-src 'self'. That directive matters more here than anywhere
// else: this is the one surface serving an asset Thane did not write,
// so the last thing its policy should permit is inline script.
Scalar.createApiReference('#app', {
  sources: [
    { url: '/docs/openapi/native.yaml', title: 'Thane Native API', slug: 'native' },
    { url: '/docs/openapi/compat.yaml', title: 'Compatibility API', slug: 'compat' },
  ],
  // Default the "Try it" code samples to curl — the operator's lingua
  // franca for a self-hosted API.
  defaultHttpClient: { targetKey: 'shell', clientKey: 'curl' },
  // Label the auto-rendered components/schemas section "Schemas" so it no
  // longer reads as a bare "Models" — that title collided with the
  // "Model Routing" tag and the "Routing & Telemetry" group.
  modelsSectionLabel: 'Schemas',
})
