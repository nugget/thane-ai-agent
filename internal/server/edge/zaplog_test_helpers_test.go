package edge

import "go.uber.org/zap"

// zapString keeps the zap import confined to one test helper.
func zapString(key, value string) zap.Field { return zap.String(key, value) }
