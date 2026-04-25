package iterate

func toolDefsNames(defs []map[string]any) []string {
	if len(defs) == 0 {
		return nil
	}
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		fn, _ := def["function"].(map[string]any)
		if fn != nil {
			if name, ok := fn["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}
