package loop

// FindDefinition returns the snapshot entry whose name matches, if present.
// A snapshot holds a small, bounded set of definitions, so a linear scan
// is fine; this lives here so the tool and HTTP surfaces share one lookup
// instead of each re-rolling it.
func FindDefinition(snapshot *DefinitionRegistrySnapshot, name string) (DefinitionSnapshot, bool) {
	if snapshot == nil {
		return DefinitionSnapshot{}, false
	}
	for _, def := range snapshot.Definitions {
		if def.Name == name {
			return def, true
		}
	}
	return DefinitionSnapshot{}, false
}

// FindDefinitionView returns the view entry whose name matches, if present.
func FindDefinitionView(view *DefinitionRegistryView, name string) (DefinitionView, bool) {
	if view == nil {
		return DefinitionView{}, false
	}
	for _, def := range view.Definitions {
		if def.Name == name {
			return def, true
		}
	}
	return DefinitionView{}, false
}
