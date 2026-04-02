package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nugget/thane-ai-agent/internal/awareness"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

const (
	defaultHARegistrySearchLimit = 8
	maxHARegistrySearchLimit     = 25
	defaultHAAutomationListLimit = 25
	maxHAAutomationListLimit     = 100
	maxHAToolResultBytes         = 50 * 1024
	automationEntityWait         = 5 * time.Second
	automationActivityHourWindow = time.Hour
	automationActivityDayWindow  = 24 * time.Hour
	automationActivityWeekWindow = 7 * 24 * time.Hour
	maxAutomationRecentHits      = 3
)

type namedID struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type haRegistrySearchResult struct {
	Query    string                  `json:"query"`
	Areas    []haRegistryAreaMatch   `json:"areas,omitempty"`
	Labels   []haRegistryLabelMatch  `json:"labels,omitempty"`
	Devices  []haRegistryDeviceMatch `json:"devices,omitempty"`
	Entities []haRegistryEntityMatch `json:"entities,omitempty"`
}

type haRegistryAreaMatch struct {
	AreaID  string    `json:"area_id"`
	Name    string    `json:"name"`
	Aliases []string  `json:"aliases,omitempty"`
	Labels  []namedID `json:"labels,omitempty"`
	Icon    string    `json:"icon,omitempty"`
	Score   float64   `json:"score"`
}

type haRegistryLabelMatch struct {
	LabelID     string  `json:"label_id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Icon        string  `json:"icon,omitempty"`
	Color       string  `json:"color,omitempty"`
	Score       float64 `json:"score"`
}

type haRegistryDeviceMatch struct {
	DeviceID      string    `json:"device_id"`
	Name          string    `json:"name,omitempty"`
	NameByUser    string    `json:"name_by_user,omitempty"`
	Manufacturer  string    `json:"manufacturer,omitempty"`
	Model         string    `json:"model,omitempty"`
	AreaID        string    `json:"area_id,omitempty"`
	AreaName      string    `json:"area_name,omitempty"`
	LabelIDs      []string  `json:"label_ids,omitempty"`
	Labels        []namedID `json:"labels,omitempty"`
	Disabled      bool      `json:"disabled"`
	Configuration string    `json:"configuration_url,omitempty"`
	Score         float64   `json:"score"`
}

type haRegistryEntityMatch struct {
	EntityID       string    `json:"entity_id"`
	Domain         string    `json:"domain,omitempty"`
	Name           string    `json:"name,omitempty"`
	OriginalName   string    `json:"original_name,omitempty"`
	FriendlyName   string    `json:"friendly_name,omitempty"`
	DeviceID       string    `json:"device_id,omitempty"`
	DeviceName     string    `json:"device_name,omitempty"`
	AreaID         string    `json:"area_id,omitempty"`
	AreaName       string    `json:"area_name,omitempty"`
	LabelIDs       []string  `json:"label_ids,omitempty"`
	Labels         []namedID `json:"labels,omitempty"`
	State          string    `json:"state,omitempty"`
	Disabled       bool      `json:"disabled"`
	Hidden         bool      `json:"hidden"`
	EntityCategory string    `json:"entity_category,omitempty"`
	Platform       string    `json:"platform,omitempty"`
	Score          float64   `json:"score"`
}

type haAutomationView struct {
	ID            string                  `json:"id"`
	EntityID      string                  `json:"entity_id,omitempty"`
	Alias         string                  `json:"alias,omitempty"`
	Description   string                  `json:"description,omitempty"`
	State         string                  `json:"state,omitempty"`
	Enabled       bool                    `json:"enabled"`
	LastTriggered string                  `json:"last_triggered,omitempty"`
	Activity      *haAutomationActivity   `json:"activity,omitempty"`
	Metadata      *haAutomationEntityMeta `json:"metadata,omitempty"`
	Config        map[string]any          `json:"config,omitempty"`
}

type haAutomationActivity struct {
	Activations1h          int      `json:"activations_1h"`
	Activations24h         int      `json:"activations_24h"`
	Activations7d          int      `json:"activations_7d"`
	ActivationRate7dPerDay float64  `json:"activation_rate_7d_per_day"`
	RecentActivations      []string `json:"recent_activations,omitempty"`
}

type haAutomationEntityMeta struct {
	Name          string            `json:"name,omitempty"`
	AreaID        string            `json:"area_id,omitempty"`
	AreaName      string            `json:"area_name,omitempty"`
	LabelIDs      []string          `json:"label_ids,omitempty"`
	Labels        []namedID         `json:"labels,omitempty"`
	Category      string            `json:"category,omitempty"`
	Categories    map[string]string `json:"categories,omitempty"`
	Icon          string            `json:"icon,omitempty"`
	Aliases       []string          `json:"aliases,omitempty"`
	HiddenBy      string            `json:"hidden_by,omitempty"`
	DisabledBy    string            `json:"disabled_by,omitempty"`
	HasEntityName bool              `json:"has_entity_name,omitempty"`
}

type resolvedAutomation struct {
	id       string
	entityID string
	state    *homeassistant.State
	entry    *homeassistant.EntityRegistryEntry
	config   map[string]any
}

func (r *Registry) registerHAAutomationTools() {
	if r.ha == nil || !r.ha.HasWSClient() {
		return
	}

	r.Register(&Tool{
		Name:        "ha_registry_search",
		Description: "Search Home Assistant registry metadata across areas, labels, devices, and entities. Use this before authoring automations so triggers, conditions, actions, area placement, and labels use polished Home Assistant-native names and IDs instead of guesses.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Human search phrase such as 'door lock battery', 'breakfast room', 'zwave deadbolts', or 'garage labels'.",
				},
				"kinds": map[string]any{
					"type":        "array",
					"description": "Optional subset to search: areas, labels, devices, entities. Default searches all four.",
					"items":       map[string]any{"type": "string", "enum": []string{"areas", "labels", "devices", "entities"}},
				},
				"domain": map[string]any{
					"type":        "string",
					"description": "Optional entity domain filter such as sensor, lock, automation, or binary_sensor.",
				},
				"area_id": map[string]any{
					"type":        "string",
					"description": "Optional area filter for devices/entities.",
				},
				"label_id": map[string]any{
					"type":        "string",
					"description": "Optional label filter for devices/entities/areas.",
				},
				"include_disabled": map[string]any{
					"type":        "boolean",
					"description": "Include disabled devices/entities. Default false.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum matches to return per kind (default %d, max %d).", defaultHARegistrySearchLimit, maxHARegistrySearchLimit),
				},
			},
			"required": []string{"query"},
		},
		Handler: r.handleHARegistrySearch,
	})

	r.Register(&Tool{
		Name:        "ha_automation_list",
		Description: "List Home Assistant automations with their config IDs, entity IDs, current enabled state, recent trigger activity (1h/24h/7d counts plus recent activation deltas), and registry metadata such as area and labels. Use this before updating or deleting an automation, and to spot automations that never fire or fire too often.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Optional search phrase against automation alias, ID, entity ID, area, and labels.",
				},
				"area_id": map[string]any{
					"type":        "string",
					"description": "Optional area filter.",
				},
				"label_id": map[string]any{
					"type":        "string",
					"description": "Optional label filter.",
				},
				"include_disabled": map[string]any{
					"type":        "boolean",
					"description": "Include disabled automations. Default false.",
				},
				"include_config": map[string]any{
					"type":        "boolean",
					"description": "Include the raw automation config for each match. Default false.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum automations to return (default %d, max %d).", defaultHAAutomationListLimit, maxHAAutomationListLimit),
				},
			},
		},
		Handler: r.handleHAAutomationList,
	})

	r.Register(&Tool{
		Name:        "ha_automation_get",
		Description: "Fetch a Home Assistant automation by config ID or entity_id. Returns the raw automation object plus registry metadata such as area, labels, icon, aliases, and current enabled state.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Automation config ID from automations.yaml, such as door-lock-battery-level-critical.",
				},
				"entity_id": map[string]any{
					"type":        "string",
					"description": "Automation entity_id such as automation.door_lock_battery_level_critical; use the automation's actual entity_id value.",
				},
			},
		},
		Handler: r.handleHAAutomationGet,
	})

	r.Register(&Tool{
		Name:        "ha_automation_create",
		Description: "Create a Home Assistant automation directly from a raw HA automation object. Supports the full depth of triggers, conditions, actions, variables, mode, max, and use_blueprint. Optional metadata can set area, labels, icon, aliases, category, hidden state, and entity_id rename after creation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Optional config ID. If omitted, Thane generates a readable slug from config.alias and avoids collisions.",
				},
				"config": map[string]any{
					"type":        "object",
					"description": "Raw Home Assistant automation config object. Preserve HA-native field names such as alias, description, triggers, conditions, actions, variables, mode, max, or use_blueprint.",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Optional entity registry metadata. Friendly fields: area_id, label_ids, category, icon, name, entity_id, aliases, hidden. Additional raw entity registry update keys may also be supplied.",
				},
				"enabled": map[string]any{
					"type":        "boolean",
					"description": "Optional post-create enabled state. Defaults to Home Assistant's normal behavior.",
				},
				"validate_only": map[string]any{
					"type":        "boolean",
					"description": "Validate trigger/condition/action sections and return the planned ID/metadata without saving.",
				},
			},
			"required": []string{"config"},
		},
		Handler: r.handleHAAutomationCreate,
	})

	r.Register(&Tool{
		Name:        "ha_automation_update",
		Description: "Update a Home Assistant automation by config ID or entity_id. Config updates are merged shallowly over the current raw automation object, then saved back through Home Assistant. Metadata updates apply via the entity registry.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Automation config ID.",
				},
				"entity_id": map[string]any{
					"type":        "string",
					"description": "Automation entity_id.",
				},
				"config": map[string]any{
					"type":        "object",
					"description": "Partial raw automation config to merge over the current one.",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Optional entity registry metadata update. Friendly fields: area_id, label_ids, category, icon, name, entity_id, aliases, hidden. Additional raw entity registry update keys may also be supplied.",
				},
				"enabled": map[string]any{
					"type":        "boolean",
					"description": "Optional post-update enabled state.",
				},
				"validate_only": map[string]any{
					"type":        "boolean",
					"description": "Validate the merged trigger/condition/action sections and return the planned changes without saving.",
				},
			},
		},
		Handler: r.handleHAAutomationUpdate,
	})

	r.Register(&Tool{
		Name:        "ha_automation_delete",
		Description: "Delete a Home Assistant automation by config ID or entity_id. This removes the automation config through Home Assistant and lets Home Assistant clean up the corresponding automation entity.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Automation config ID.",
				},
				"entity_id": map[string]any{
					"type":        "string",
					"description": "Automation entity_id.",
				},
			},
		},
		Handler: r.handleHAAutomationDelete,
	})
}

func (r *Registry) handleHARegistrySearch(ctx context.Context, args map[string]any) (string, error) {
	if err := ensureHAAvailable(r.ha); err != nil {
		return "", err
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	limit, err := boundedIntArg(args, "limit", defaultHARegistrySearchLimit, maxHARegistrySearchLimit)
	if err != nil {
		return "", err
	}

	kinds := stringSliceArg(args, "kinds")
	searchKinds := make(map[string]bool)
	if len(kinds) == 0 {
		searchKinds["areas"] = true
		searchKinds["labels"] = true
		searchKinds["devices"] = true
		searchKinds["entities"] = true
	} else {
		for _, kind := range kinds {
			searchKinds[kind] = true
		}
	}

	areaFilter := strings.TrimSpace(stringArg(args, "area_id"))
	labelFilter := strings.TrimSpace(stringArg(args, "label_id"))
	domainFilter := strings.TrimSpace(stringArg(args, "domain"))
	includeDisabled := boolArg(args, "include_disabled")

	needAreas := searchKinds["areas"] || searchKinds["devices"] || searchKinds["entities"]
	needLabels := searchKinds["areas"] || searchKinds["labels"] || searchKinds["devices"] || searchKinds["entities"]
	needDevices := searchKinds["devices"] || searchKinds["entities"]
	needEntities := searchKinds["entities"]
	needStates := searchKinds["entities"]

	var areas []homeassistant.Area
	if needAreas {
		areas, err = r.ha.GetAreas(ctx)
		if err != nil {
			return "", fmt.Errorf("get areas: %w", err)
		}
	}

	var labels []homeassistant.LabelRegistryEntry
	if needLabels {
		labels, err = r.ha.GetLabelRegistry(ctx)
		if err != nil {
			return "", fmt.Errorf("get labels: %w", err)
		}
	}

	var devices []homeassistant.DeviceRegistryEntry
	if needDevices {
		devices, err = r.ha.GetDeviceRegistry(ctx)
		if err != nil {
			return "", fmt.Errorf("get devices: %w", err)
		}
	}

	var entities []homeassistant.EntityRegistryEntry
	if needEntities {
		entities, err = r.ha.GetEntityRegistry(ctx)
		if err != nil {
			return "", fmt.Errorf("get entity registry: %w", err)
		}
	}

	var states []homeassistant.State
	if needStates {
		states, err = r.ha.GetStates(ctx)
		if err != nil {
			return "", fmt.Errorf("get states: %w", err)
		}
	}

	labelMap := buildLabelNameMap(labels)
	areaMap := buildAreaNameMap(areas)
	stateMap := buildStateMap(states)
	deviceMap := buildDeviceMap(devices)

	result := haRegistrySearchResult{Query: query}
	queryScore := makeScorer(query)

	if searchKinds["areas"] {
		type scoredArea struct {
			match haRegistryAreaMatch
		}
		var matches []scoredArea
		for _, area := range areas {
			if labelFilter != "" && !containsString(area.Labels, labelFilter) {
				continue
			}
			score := queryScore(area.Name, strings.Join(area.Aliases, " "))
			if score <= 0 {
				continue
			}
			matches = append(matches, scoredArea{
				match: haRegistryAreaMatch{
					AreaID:  area.AreaID,
					Name:    area.Name,
					Aliases: area.Aliases,
					Labels:  resolveNamedIDs(area.Labels, labelMap),
					Icon:    area.Icon,
					Score:   score,
				},
			})
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].match.Score > matches[j].match.Score })
		for i, item := range matches {
			if i >= limit {
				break
			}
			result.Areas = append(result.Areas, item.match)
		}
	}

	if searchKinds["labels"] {
		type scoredLabel struct {
			match haRegistryLabelMatch
		}
		var matches []scoredLabel
		for _, label := range labels {
			score := queryScore(label.Name, label.Description)
			if score <= 0 {
				continue
			}
			matches = append(matches, scoredLabel{
				match: haRegistryLabelMatch{
					LabelID:     label.LabelID,
					Name:        label.Name,
					Description: label.Description,
					Icon:        label.Icon,
					Color:       label.Color,
					Score:       score,
				},
			})
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].match.Score > matches[j].match.Score })
		for i, item := range matches {
			if i >= limit {
				break
			}
			result.Labels = append(result.Labels, item.match)
		}
	}

	if searchKinds["devices"] {
		type scoredDevice struct {
			match haRegistryDeviceMatch
		}
		var matches []scoredDevice
		for _, device := range devices {
			if areaFilter != "" && device.AreaID != areaFilter {
				continue
			}
			if labelFilter != "" && !containsString(device.Labels, labelFilter) {
				continue
			}
			if !includeDisabled && device.DisabledBy != "" {
				continue
			}
			score := queryScore(
				device.NameByUser,
				device.Name,
				device.Manufacturer,
				device.Model,
				device.SerialNumber,
				areaMap[device.AreaID],
				strings.Join(resolveNames(device.Labels, labelMap), " "),
			)
			if score <= 0 {
				continue
			}
			matches = append(matches, scoredDevice{
				match: haRegistryDeviceMatch{
					DeviceID:      device.ID,
					Name:          device.Name,
					NameByUser:    device.NameByUser,
					Manufacturer:  device.Manufacturer,
					Model:         device.Model,
					AreaID:        device.AreaID,
					AreaName:      areaMap[device.AreaID],
					LabelIDs:      device.Labels,
					Labels:        resolveNamedIDs(device.Labels, labelMap),
					Disabled:      device.DisabledBy != "",
					Configuration: device.ConfigurationURL,
					Score:         score,
				},
			})
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].match.Score > matches[j].match.Score })
		for i, item := range matches {
			if i >= limit {
				break
			}
			result.Devices = append(result.Devices, item.match)
		}
	}

	if searchKinds["entities"] {
		type scoredEntity struct {
			match haRegistryEntityMatch
		}
		var matches []scoredEntity
		for _, entity := range entities {
			domain := entityDomain(entity.EntityID)
			if domainFilter != "" && domain != domainFilter {
				continue
			}
			if areaFilter != "" && entity.AreaID != areaFilter {
				continue
			}
			if labelFilter != "" && !containsString(entity.Labels, labelFilter) {
				continue
			}
			if !includeDisabled && entity.DisabledBy != "" {
				continue
			}

			deviceName := ""
			if device := deviceMap[entity.DeviceID]; device != nil {
				deviceName = coalesce(device.NameByUser, device.Name)
			}
			friendlyName := friendlyNameForState(stateMap[entity.EntityID])
			score := queryScore(
				entity.EntityID,
				entity.Name,
				entity.OriginalName,
				strings.Join(entity.Aliases, " "),
				friendlyName,
				deviceName,
				areaMap[entity.AreaID],
				strings.Join(resolveNames(entity.Labels, labelMap), " "),
			)
			if score <= 0 {
				continue
			}
			state := ""
			if st := stateMap[entity.EntityID]; st != nil {
				state = st.State
			}
			matches = append(matches, scoredEntity{
				match: haRegistryEntityMatch{
					EntityID:       entity.EntityID,
					Domain:         domain,
					Name:           entity.Name,
					OriginalName:   entity.OriginalName,
					FriendlyName:   friendlyName,
					DeviceID:       entity.DeviceID,
					DeviceName:     deviceName,
					AreaID:         entity.AreaID,
					AreaName:       areaMap[entity.AreaID],
					LabelIDs:       entity.Labels,
					Labels:         resolveNamedIDs(entity.Labels, labelMap),
					State:          state,
					Disabled:       entity.DisabledBy != "",
					Hidden:         entity.HiddenBy != "",
					EntityCategory: entity.EntityCategory,
					Platform:       entity.Platform,
					Score:          score,
				},
			})
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].match.Score > matches[j].match.Score })
		for i, item := range matches {
			if i >= limit {
				break
			}
			result.Entities = append(result.Entities, item.match)
		}
	}

	return toIndentedJSON(result), nil
}

func (r *Registry) handleHAAutomationList(ctx context.Context, args map[string]any) (string, error) {
	if err := ensureHAAvailable(r.ha); err != nil {
		return "", err
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	limit, err := boundedIntArg(args, "limit", defaultHAAutomationListLimit, maxHAAutomationListLimit)
	if err != nil {
		return "", err
	}

	query := strings.TrimSpace(stringArg(args, "query"))
	areaFilter := strings.TrimSpace(stringArg(args, "area_id"))
	labelFilter := strings.TrimSpace(stringArg(args, "label_id"))
	includeDisabled := boolArg(args, "include_disabled")
	includeConfig := boolArg(args, "include_config")

	views, err := r.listAutomations(ctx, includeConfig)
	if err != nil {
		return "", err
	}

	if query != "" {
		scorer := makeScorer(query)
		type scored struct {
			view  haAutomationView
			score float64
		}
		var filtered []scored
		for _, view := range views {
			areaName := ""
			labelNames := []string(nil)
			if view.Metadata != nil {
				areaName = view.Metadata.AreaName
				labelNames = namedIDNames(view.Metadata.Labels)
			}
			score := scorer(
				view.ID,
				view.EntityID,
				view.Alias,
				view.Description,
				areaName,
				strings.Join(labelNames, " "),
			)
			if score <= 0 {
				continue
			}
			filtered = append(filtered, scored{view: view, score: score})
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].score > filtered[j].score })
		views = views[:0]
		for _, item := range filtered {
			views = append(views, item.view)
		}
	}

	var filtered []haAutomationView
	for _, view := range views {
		if view.Metadata != nil {
			if areaFilter != "" && view.Metadata.AreaID != areaFilter {
				continue
			}
			if labelFilter != "" && !containsString(view.Metadata.LabelIDs, labelFilter) {
				continue
			}
			if !includeDisabled && view.Metadata.DisabledBy != "" {
				continue
			}
		} else if areaFilter != "" || labelFilter != "" {
			continue
		}
		filtered = append(filtered, view)
		if len(filtered) >= limit {
			break
		}
	}

	if err := r.enrichAutomationActivity(ctx, filtered); err != nil {
		return "", err
	}

	return toIndentedJSON(filtered), nil
}

func (r *Registry) handleHAAutomationGet(ctx context.Context, args map[string]any) (string, error) {
	if err := ensureHAAvailable(r.ha); err != nil {
		return "", err
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	resolved, err := r.resolveAutomation(ctx, stringArg(args, "id"), stringArg(args, "entity_id"))
	if err != nil {
		return "", err
	}
	view, err := r.buildAutomationView(ctx, resolved, true)
	if err != nil {
		return "", err
	}
	return toIndentedJSON(view), nil
}

func (r *Registry) handleHAAutomationCreate(ctx context.Context, args map[string]any) (string, error) {
	if err := ensureHAAvailable(r.ha); err != nil {
		return "", err
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	cfg := mapArg(args, "config")
	if cfg == nil {
		return "", fmt.Errorf("config is required")
	}

	alias := strings.TrimSpace(stringFromAny(cfg["alias"]))
	if alias == "" {
		return "", fmt.Errorf("config.alias is required for create")
	}

	id := strings.TrimSpace(stringArg(args, "id"))
	if id == "" {
		generatedID, err := r.nextAvailableAutomationID(ctx, automationIDSlug(alias))
		if err != nil {
			return "", err
		}
		id = generatedID
	}

	validation, err := r.validateAutomationConfig(ctx, cfg)
	if err != nil {
		return "", err
	}

	metadataUpdate, err := buildEntityRegistryUpdate(mapArg(args, "metadata"))
	if err != nil {
		return "", err
	}

	if boolArg(args, "validate_only") {
		return toIndentedJSON(map[string]any{
			"id":         id,
			"config":     cfg,
			"metadata":   metadataUpdate,
			"validation": validation,
		}), nil
	}

	if err := r.ha.SaveAutomationConfig(ctx, id, cfg); err != nil {
		return "", fmt.Errorf("save automation config: %w", err)
	}

	entityID, err := r.waitForAutomationEntity(ctx, id)
	if err != nil && (len(metadataUpdate) > 0 || hasArg(args, "enabled")) {
		return "", fmt.Errorf("automation saved but entity not yet available for metadata updates: %w", err)
	}

	if len(metadataUpdate) > 0 && entityID != "" {
		entry, err := r.ha.UpdateEntityRegistryEntry(ctx, entityID, metadataUpdate)
		if err != nil {
			return "", fmt.Errorf("update automation metadata: %w", err)
		}
		if metadataUpdate["new_entity_id"] != nil && entry != nil && entry.EntityID != "" {
			entityID = entry.EntityID
		}
	}

	if hasArg(args, "enabled") && entityID != "" {
		enabled := boolArg(args, "enabled")
		if err := r.ha.ApplyAutomationEnabledState(ctx, entityID, enabled); err != nil {
			return "", fmt.Errorf("set automation enabled state: %w", err)
		}
	}

	view, err := r.resolveAutomation(ctx, id, entityID)
	if err != nil {
		return toIndentedJSON(map[string]any{
			"id":        id,
			"entity_id": entityID,
			"saved":     true,
		}), nil
	}
	result, err := r.buildAutomationView(ctx, view, true)
	if err != nil {
		return "", err
	}
	return toIndentedJSON(result), nil
}

func (r *Registry) handleHAAutomationUpdate(ctx context.Context, args map[string]any) (string, error) {
	if err := ensureHAAvailable(r.ha); err != nil {
		return "", err
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	resolved, err := r.resolveAutomation(ctx, stringArg(args, "id"), stringArg(args, "entity_id"))
	if err != nil {
		return "", err
	}

	configPatch := mapArg(args, "config")
	metadataUpdate, err := buildEntityRegistryUpdate(mapArg(args, "metadata"))
	if err != nil {
		return "", err
	}
	if configPatch == nil && len(metadataUpdate) == 0 && !hasArg(args, "enabled") {
		return "", fmt.Errorf("at least one of config, metadata, or enabled is required")
	}

	mergedConfig := resolved.config
	var validation map[string]homeassistant.ConfigValidationResult
	if configPatch != nil {
		mergedConfig = mergeAutomationConfig(resolved.config, configPatch)
		validation, err = r.validateAutomationConfig(ctx, mergedConfig)
		if err != nil {
			return "", err
		}
	}

	if boolArg(args, "validate_only") {
		return toIndentedJSON(map[string]any{
			"id":         resolved.id,
			"entity_id":  resolved.entityID,
			"config":     mergedConfig,
			"metadata":   metadataUpdate,
			"validation": validation,
		}), nil
	}

	if configPatch != nil {
		if err := r.ha.SaveAutomationConfig(ctx, resolved.id, mergedConfig); err != nil {
			return "", fmt.Errorf("save automation config: %w", err)
		}
	}

	entityID := resolved.entityID
	if entityID == "" && (len(metadataUpdate) > 0 || hasArg(args, "enabled")) {
		entityID, err = r.waitForAutomationEntity(ctx, resolved.id)
		if err != nil {
			return "", fmt.Errorf("automation config updated but entity not yet available for metadata updates: %w", err)
		}
	}

	if len(metadataUpdate) > 0 && entityID != "" {
		entry, err := r.ha.UpdateEntityRegistryEntry(ctx, entityID, metadataUpdate)
		if err != nil {
			return "", fmt.Errorf("update automation metadata: %w", err)
		}
		if metadataUpdate["new_entity_id"] != nil && entry != nil && entry.EntityID != "" {
			entityID = entry.EntityID
		}
	}

	if hasArg(args, "enabled") && entityID != "" {
		if err := r.ha.ApplyAutomationEnabledState(ctx, entityID, boolArg(args, "enabled")); err != nil {
			return "", fmt.Errorf("set automation enabled state: %w", err)
		}
	}

	updated, err := r.resolveAutomation(ctx, resolved.id, entityID)
	if err != nil {
		return "", err
	}
	view, err := r.buildAutomationView(ctx, updated, true)
	if err != nil {
		return "", err
	}
	return toIndentedJSON(view), nil
}

func (r *Registry) handleHAAutomationDelete(ctx context.Context, args map[string]any) (string, error) {
	if err := ensureHAAvailable(r.ha); err != nil {
		return "", err
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	resolved, err := r.resolveAutomation(ctx, stringArg(args, "id"), stringArg(args, "entity_id"))
	if err != nil {
		return "", err
	}
	if err := r.ha.DeleteAutomationConfig(ctx, resolved.id); err != nil {
		return "", fmt.Errorf("delete automation config: %w", err)
	}
	return toIndentedJSON(map[string]any{
		"deleted":   true,
		"id":        resolved.id,
		"entity_id": resolved.entityID,
	}), nil
}

func (r *Registry) listAutomations(ctx context.Context, includeConfig bool) ([]haAutomationView, error) {
	states, err := r.ha.GetStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("get states: %w", err)
	}
	entries, err := r.ha.GetEntityRegistry(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entity registry: %w", err)
	}

	entryByEntity := make(map[string]homeassistant.EntityRegistryEntry, len(entries))
	for _, entry := range entries {
		if entityDomain(entry.EntityID) == "automation" {
			entryByEntity[entry.EntityID] = entry
		}
	}
	areaMap, labelMap, err := r.loadAreaAndLabelMaps(ctx)
	if err != nil {
		return nil, err
	}

	var views []haAutomationView
	for _, state := range states {
		if entityDomain(state.EntityID) != "automation" {
			continue
		}
		id := automationIDFromState(&state)
		if id == "" {
			continue
		}
		resolved := resolvedAutomation{
			id:       id,
			entityID: state.EntityID,
			state:    &state,
			config:   nil,
		}
		if entry, ok := entryByEntity[state.EntityID]; ok {
			entryCopy := entry
			resolved.entry = &entryCopy
		}
		if includeConfig {
			cfg, err := r.ha.GetAutomationConfigByID(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("get automation config %s: %w", id, err)
			}
			resolved.config = cfg
		}
		view := buildAutomationViewFromMaps(resolved, includeConfig, areaMap, labelMap)
		views = append(views, view)
	}

	sort.Slice(views, func(i, j int) bool { return views[i].Alias < views[j].Alias })
	return views, nil
}

func (r *Registry) enrichAutomationActivity(ctx context.Context, views []haAutomationView) error {
	if len(views) == 0 {
		return nil
	}

	entityIDs := make([]string, 0, len(views))
	seen := make(map[string]struct{}, len(views))
	for _, view := range views {
		entityID := strings.TrimSpace(view.EntityID)
		if entityID == "" {
			continue
		}
		if _, ok := seen[entityID]; ok {
			continue
		}
		seen[entityID] = struct{}{}
		entityIDs = append(entityIDs, entityID)
	}
	if len(entityIDs) == 0 {
		return nil
	}

	now := time.Now()
	activityByEntity, err := r.loadAutomationActivity(ctx, entityIDs, now)
	if err != nil {
		if isHALogbookUnsupported(err) {
			return nil
		}
		return fmt.Errorf("load automation activity: %w", err)
	}

	for i := range views {
		summary, ok := activityByEntity[views[i].EntityID]
		if !ok {
			summary = summarizeAutomationActivity(nil, now)
		}
		views[i].Activity = &summary
	}

	return nil
}

func (r *Registry) loadAutomationActivity(ctx context.Context, entityIDs []string, now time.Time) (map[string]haAutomationActivity, error) {
	events, err := r.ha.GetLogbookEvents(ctx, now.Add(-automationActivityWeekWindow), now, entityIDs)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]struct{}, len(entityIDs))
	for _, entityID := range entityIDs {
		wanted[entityID] = struct{}{}
	}

	timesByEntity := make(map[string][]time.Time, len(entityIDs))
	for _, entry := range events {
		if !isAutomationTriggerLogbookEntry(entry) {
			continue
		}
		if _, ok := wanted[entry.EntityID]; !ok {
			continue
		}
		when := entry.WhenTime()
		if when.IsZero() {
			continue
		}
		timesByEntity[entry.EntityID] = append(timesByEntity[entry.EntityID], when)
	}

	summaries := make(map[string]haAutomationActivity, len(entityIDs))
	for _, entityID := range entityIDs {
		summaries[entityID] = summarizeAutomationActivity(timesByEntity[entityID], now)
	}

	return summaries, nil
}

func (r *Registry) resolveAutomation(ctx context.Context, idArg, entityArg string) (resolvedAutomation, error) {
	idArg = strings.TrimSpace(idArg)
	entityArg = strings.TrimSpace(entityArg)
	if idArg == "" && entityArg == "" {
		return resolvedAutomation{}, fmt.Errorf("id or entity_id is required")
	}

	states, err := r.ha.GetStates(ctx)
	if err != nil {
		return resolvedAutomation{}, fmt.Errorf("get states: %w", err)
	}

	var resolved resolvedAutomation
	for i := range states {
		state := states[i]
		if entityDomain(state.EntityID) != "automation" {
			continue
		}
		stateID := automationIDFromState(&state)
		if entityArg != "" && state.EntityID == entityArg {
			resolved.entityID = state.EntityID
			resolved.id = stateID
			stateCopy := state
			resolved.state = &stateCopy
			break
		}
		if idArg != "" && stateID == idArg {
			resolved.entityID = state.EntityID
			resolved.id = stateID
			stateCopy := state
			resolved.state = &stateCopy
			break
		}
	}

	if resolved.id == "" && idArg != "" {
		resolved.id = idArg
	}
	if resolved.entityID == "" && entityArg != "" {
		resolved.entityID = entityArg
	}

	if resolved.id == "" && resolved.entityID != "" {
		cfg, err := r.ha.GetAutomationConfigByEntityID(ctx, resolved.entityID)
		if err != nil {
			return resolvedAutomation{}, fmt.Errorf("resolve automation by entity_id: %w", err)
		}
		resolved.config = cfg
		if state := resolved.state; state != nil {
			resolved.id = automationIDFromState(state)
		}
	}

	if resolved.id == "" {
		return resolvedAutomation{}, fmt.Errorf("could not resolve automation config id")
	}

	if resolved.config == nil {
		cfg, err := r.ha.GetAutomationConfigByID(ctx, resolved.id)
		if err != nil {
			return resolvedAutomation{}, fmt.Errorf("get automation config %s: %w", resolved.id, err)
		}
		resolved.config = cfg
	}

	if resolved.entityID != "" {
		entry, err := r.ha.GetEntityRegistryEntry(ctx, resolved.entityID)
		if err != nil {
			if !isEntityRegistryNotFound(err) {
				return resolvedAutomation{}, fmt.Errorf("get automation metadata: %w", err)
			}
		} else {
			resolved.entry = entry
		}
	}

	return resolved, nil
}

func (r *Registry) buildAutomationView(ctx context.Context, resolved resolvedAutomation, includeConfig bool) (haAutomationView, error) {
	areaMap, labelMap, err := r.loadAreaAndLabelMaps(ctx)
	if err != nil {
		return haAutomationView{}, err
	}
	return buildAutomationViewFromMaps(resolved, includeConfig, areaMap, labelMap), nil
}

func (r *Registry) validateAutomationConfig(ctx context.Context, cfg map[string]any) (map[string]homeassistant.ConfigValidationResult, error) {
	sections := validationPayload(cfg)
	if len(sections) == 0 {
		return nil, nil
	}
	validation, err := r.ha.ValidateConfig(ctx, sections)
	if err != nil {
		return nil, fmt.Errorf("validate automation config: %w", err)
	}
	var failures []string
	for key, result := range validation {
		if !result.Valid {
			failures = append(failures, fmt.Sprintf("%s: %s", key, result.Error))
		}
	}
	sort.Strings(failures)
	if len(failures) > 0 {
		return validation, fmt.Errorf("automation config validation failed: %s", strings.Join(failures, "; "))
	}
	return validation, nil
}

func validationPayload(cfg map[string]any) map[string]any {
	sections := make(map[string]any)
	if v, ok := cfg["triggers"]; ok {
		sections["triggers"] = v
	} else if v, ok := cfg["trigger"]; ok {
		sections["triggers"] = v
	}
	if v, ok := cfg["conditions"]; ok {
		sections["conditions"] = v
	} else if v, ok := cfg["condition"]; ok {
		sections["conditions"] = v
	}
	if v, ok := cfg["actions"]; ok {
		sections["actions"] = v
	} else if v, ok := cfg["action"]; ok {
		sections["actions"] = v
	}
	return sections
}

func (r *Registry) waitForAutomationEntity(ctx context.Context, id string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, automationEntityWait)
	defer cancel()

	delay := 250 * time.Millisecond
	const maxDelay = time.Second

	for {
		states, err := r.ha.GetStates(waitCtx)
		if err != nil {
			return "", fmt.Errorf("get states: %w", err)
		}
		for _, state := range states {
			if entityDomain(state.EntityID) != "automation" {
				continue
			}
			if automationIDFromState(&state) == id {
				return state.EntityID, nil
			}
		}

		select {
		case <-waitCtx.Done():
			return "", waitCtx.Err()
		case <-time.After(delay):
			if delay < maxDelay {
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
			}
		}
	}
}

func (r *Registry) nextAvailableAutomationID(ctx context.Context, base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = fmt.Sprintf("automation-%d", time.Now().Unix())
	}
	candidate := base
	for i := 2; i < 1000; i++ {
		_, err := r.ha.GetAutomationConfigByID(ctx, candidate)
		if err == nil {
			candidate = fmt.Sprintf("%s-%d", base, i)
			continue
		}
		var apiErr *homeassistant.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return candidate, nil
		}
		return "", fmt.Errorf("check automation id availability: %w", err)
	}
	return "", fmt.Errorf("could not find available automation id for base %q", base)
}

func (r *Registry) loadAreaAndLabelMaps(ctx context.Context) (map[string]string, map[string]string, error) {
	areas, err := r.ha.GetAreas(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("get areas: %w", err)
	}
	labels, err := r.ha.GetLabelRegistry(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("get labels: %w", err)
	}
	return buildAreaNameMap(areas), buildLabelNameMap(labels), nil
}

func buildAutomationViewFromMaps(resolved resolvedAutomation, includeConfig bool, areaMap, labelMap map[string]string) haAutomationView {
	view := haAutomationView{
		ID:       resolved.id,
		EntityID: resolved.entityID,
		Enabled:  resolved.state != nil && resolved.state.State == "on",
	}

	if resolved.state != nil {
		view.State = resolved.state.State
		if lastTriggered, ok := resolved.state.Attributes["last_triggered"].(string); ok {
			view.LastTriggered = formatAutomationLastTriggered(lastTriggered, time.Now())
		}
	}

	if resolved.config != nil {
		view.Alias = strings.TrimSpace(stringFromAny(resolved.config["alias"]))
		view.Description = strings.TrimSpace(stringFromAny(resolved.config["description"]))
		if includeConfig {
			view.Config = resolved.config
		}
	}
	if view.Alias == "" && resolved.state != nil {
		view.Alias = friendlyNameForState(resolved.state)
	}

	if resolved.entry != nil {
		view.Metadata = &haAutomationEntityMeta{
			Name:          resolved.entry.Name,
			AreaID:        resolved.entry.AreaID,
			AreaName:      areaMap[resolved.entry.AreaID],
			LabelIDs:      append([]string(nil), resolved.entry.Labels...),
			Labels:        resolveNamedIDs(resolved.entry.Labels, labelMap),
			Categories:    resolved.entry.Categories,
			Category:      resolved.entry.Categories["automation"],
			Icon:          resolved.entry.Icon,
			Aliases:       resolved.entry.Aliases,
			HiddenBy:      resolved.entry.HiddenBy,
			DisabledBy:    resolved.entry.DisabledBy,
			HasEntityName: resolved.entry.HasEntityName,
		}
	}

	return view
}

func buildAreaNameMap(areas []homeassistant.Area) map[string]string {
	out := make(map[string]string, len(areas))
	for _, area := range areas {
		out[area.AreaID] = area.Name
	}
	return out
}

func buildLabelNameMap(labels []homeassistant.LabelRegistryEntry) map[string]string {
	out := make(map[string]string, len(labels))
	for _, label := range labels {
		out[label.LabelID] = label.Name
	}
	return out
}

func buildStateMap(states []homeassistant.State) map[string]*homeassistant.State {
	out := make(map[string]*homeassistant.State, len(states))
	for i := range states {
		out[states[i].EntityID] = &states[i]
	}
	return out
}

func buildDeviceMap(devices []homeassistant.DeviceRegistryEntry) map[string]*homeassistant.DeviceRegistryEntry {
	out := make(map[string]*homeassistant.DeviceRegistryEntry, len(devices))
	for i := range devices {
		out[devices[i].ID] = &devices[i]
	}
	return out
}

func summarizeAutomationActivity(times []time.Time, now time.Time) haAutomationActivity {
	sort.Slice(times, func(i, j int) bool { return times[i].After(times[j]) })

	weekCutoff := now.Add(-automationActivityWeekWindow)
	dayCutoff := now.Add(-automationActivityDayWindow)
	hourCutoff := now.Add(-automationActivityHourWindow)

	weekCount := countTimesSince(times, weekCutoff)
	summary := haAutomationActivity{
		Activations1h:          countTimesSince(times, hourCutoff),
		Activations24h:         countTimesSince(times, dayCutoff),
		Activations7d:          weekCount,
		ActivationRate7dPerDay: roundToHundredths(float64(weekCount) / 7),
	}

	for _, when := range times {
		if when.Before(weekCutoff) {
			continue
		}
		summary.RecentActivations = append(summary.RecentActivations, awareness.FormatDeltaOnly(when, now))
		if len(summary.RecentActivations) >= maxAutomationRecentHits {
			break
		}
	}

	return summary
}

func countTimesSince(times []time.Time, cutoff time.Time) int {
	count := 0
	for _, when := range times {
		if when.Before(cutoff) {
			break
		}
		count++
	}
	return count
}

func roundToHundredths(v float64) float64 {
	return math.Round(v*100) / 100
}

func isAutomationTriggerLogbookEntry(entry homeassistant.LogbookEntry) bool {
	if strings.TrimSpace(entry.EntityID) == "" {
		return false
	}
	if entry.Domain != "" && entry.Domain != "automation" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(entry.Message)), "triggered")
}

func isHALogbookUnsupported(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not_found:") || strings.Contains(msg, "unknown command")
}

func ensureHAAvailable(ha *homeassistant.Client) error {
	if ha == nil {
		return fmt.Errorf("home assistant not configured")
	}
	return nil
}

func boundedIntArg(args map[string]any, key string, def, max int) (int, error) {
	if args == nil {
		return def, nil
	}
	raw, ok := args[key]
	if !ok {
		return def, nil
	}
	switch v := raw.(type) {
	case float64:
		n := int(v)
		if float64(n) != v {
			return 0, fmt.Errorf("%s must be a whole number", key)
		}
		if n <= 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		if n > max {
			return 0, fmt.Errorf("%s must be <= %d", key, max)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("%s must be a whole number", key)
	}
}

func mapArg(args map[string]any, key string) map[string]any {
	if args == nil {
		return nil
	}
	raw, ok := args[key]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	v, _ := args[key].(bool)
	return v
}

func hasArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	_, ok := args[key]
	return ok
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func buildEntityRegistryUpdate(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return nil, nil
	}
	update := make(map[string]any)

	for key, value := range metadata {
		switch key {
		case "label_ids", "category", "entity_id", "hidden", "raw_update":
			continue
		default:
			update[key] = value
		}
	}

	if raw := mapArg(metadata, "raw_update"); raw != nil {
		for key, value := range raw {
			update[key] = value
		}
	}

	if hasArg(metadata, "label_ids") {
		labels := stringSliceArg(metadata, "label_ids")
		update["labels"] = labels
	}
	if hasArg(metadata, "category") {
		categories := map[string]any{}
		if existing, ok := update["categories"].(map[string]any); ok {
			for key, value := range existing {
				categories[key] = value
			}
		}
		if metadata["category"] == nil {
			categories["automation"] = nil
		} else if category := strings.TrimSpace(stringArg(metadata, "category")); category != "" {
			categories["automation"] = category
		}
		update["categories"] = categories
	}
	if newEntityID := strings.TrimSpace(stringArg(metadata, "entity_id")); newEntityID != "" {
		update["new_entity_id"] = newEntityID
	}
	if hidden, ok := metadata["hidden"].(bool); ok {
		if hidden {
			update["hidden_by"] = "user"
		} else {
			update["hidden_by"] = nil
		}
	}

	return update, nil
}

func mergeAutomationConfig(base, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range patch {
		merged[k] = v
	}
	return merged
}

func automationIDFromState(state *homeassistant.State) string {
	if state == nil {
		return ""
	}
	if id, ok := state.Attributes["id"].(string); ok {
		return id
	}
	return ""
}

func entityDomain(entityID string) string {
	if parts := strings.SplitN(entityID, ".", 2); len(parts) == 2 {
		return parts[0]
	}
	return ""
}

func friendlyNameForState(state *homeassistant.State) string {
	if state == nil {
		return ""
	}
	if friendly, ok := state.Attributes["friendly_name"].(string); ok {
		return friendly
	}
	return ""
}

func formatAutomationLastTriggered(raw string, now time.Time) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return awareness.FormatDeltaOnly(ts, now)
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return awareness.FormatDeltaOnly(ts, now)
	}
	return raw
}

func resolveNamedIDs(ids []string, names map[string]string) []namedID {
	out := make([]namedID, 0, len(ids))
	for _, id := range ids {
		out = append(out, namedID{ID: id, Name: names[id]})
	}
	return out
}

func resolveNames(ids []string, names map[string]string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if name := names[id]; name != "" {
			out = append(out, name)
		}
	}
	return out
}

func namedIDNames(ids []namedID) []string {
	out := make([]string, 0, len(ids))
	for _, item := range ids {
		if item.Name != "" {
			out = append(out, item.Name)
		}
	}
	return out
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func coalesce(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func automationIDSlug(alias string) string {
	alias = strings.ToLower(alias)
	var b strings.Builder
	lastHyphen := false
	for _, r := range alias {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastHyphen = false
		case r == '_' || r == '-':
			if !lastHyphen && b.Len() > 0 {
				b.WriteRune('-')
				lastHyphen = true
			}
		default:
			if !lastHyphen && b.Len() > 0 {
				b.WriteRune('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func makeScorer(query string) func(fields ...string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	queryTokens := tokenizeSearch(query)
	return func(fields ...string) float64 {
		if query == "" {
			return 0
		}
		joined := strings.ToLower(strings.Join(fields, " "))
		if strings.TrimSpace(joined) == "" {
			return 0
		}

		score := 0.0
		if strings.Contains(joined, query) {
			score += 1.0
		}

		fieldTokens := tokenizeSearch(joined)
		for _, token := range queryTokens {
			best := 0.0
			for _, candidate := range fieldTokens {
				switch {
				case token == candidate:
					best = max(best, 1.0)
				case strings.Contains(candidate, token) || strings.Contains(token, candidate):
					best = max(best, 0.75)
				}
			}
			score += best
		}
		if len(queryTokens) == 0 {
			return score
		}
		return score / float64(len(queryTokens)+1)
	}
}

func tokenizeSearch(s string) []string {
	replacer := strings.NewReplacer("_", " ", ".", " ", "-", " ", "/", " ", ":", " ")
	s = replacer.Replace(s)
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 1 {
			out = append(out, field)
		}
	}
	return out
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func toIndentedJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return `{"error":"json encoding failed"}`
	}
	if len(data) <= maxHAToolResultBytes {
		return string(data)
	}

	compact, err := json.Marshal(v)
	if err != nil {
		return `{"error":"json encoding failed"}`
	}
	if len(compact) <= maxHAToolResultBytes {
		return string(compact)
	}

	return encodeTruncatedJSONPreview(compact)
}

func isEntityRegistryNotFound(err error) bool {
	for err != nil {
		msg := strings.ToLower(strings.TrimSpace(err.Error()))
		if strings.HasPrefix(msg, "not_found:") || strings.Contains(msg, "not_found:") || strings.Contains(msg, "entity not found") {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func encodeTruncatedJSONPreview(compact []byte) string {
	const overhead = 256
	previewBudget := maxHAToolResultBytes - overhead
	if previewBudget < 0 {
		previewBudget = 0
	}

	for {
		envelope := map[string]any{
			"_truncated":  true,
			"total_bytes": len(compact),
			"max_bytes":   maxHAToolResultBytes,
			"note":        "Result exceeded the tool byte cap; narrow the query or disable include_config for a smaller payload.",
			"preview":     truncateUTF8(string(compact), previewBudget),
		}
		data, err := json.Marshal(envelope)
		if err == nil && len(data) <= maxHAToolResultBytes {
			return string(data)
		}
		if previewBudget == 0 {
			break
		}
		if previewBudget > 512 {
			previewBudget -= 512
		} else {
			previewBudget = 0
		}
	}

	return fmt.Sprintf(`{"_truncated":true,"total_bytes":%d,"max_bytes":%d,"note":"Result exceeded the tool byte cap; narrow the query or disable include_config for a smaller payload."}`, len(compact), maxHAToolResultBytes)
}
