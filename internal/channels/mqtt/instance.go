package mqtt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// LoadOrCreateInstanceID reads the instance ID from a file in dataDir,
// or generates a new UUIDv7 and persists it if the file does not exist.
// The instance ID is the stable HA device identifier — it survives
// renames of the device_name config field so HA entity history is
// preserved across reconfigurations.
func LoadOrCreateInstanceID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "instance_id")

	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate instance ID: %w", err)
	}

	idStr := id.String()
	if err := os.WriteFile(path, []byte(idStr+"\n"), 0644); err != nil {
		return "", fmt.Errorf("persist instance ID to %s: %w", path, err)
	}

	return idStr, nil
}
