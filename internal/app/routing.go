package app

// syncRouterConfig refreshes the live router from the current effective
// model registry catalog. It is safe to call repeatedly; audit history
// and stats stay intact while the model list/default are swapped.
func (a *App) syncRouterConfig() {
	if a == nil || a.rtr == nil || a.modelRegistry == nil {
		return
	}
	cat := a.modelRegistry.Catalog()
	if cat == nil {
		return
	}
	a.rtr.UpdateConfig(cat.RouterConfig(0))
}
