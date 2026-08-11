package mqtt

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

// StatsSource provides runtime data for sensor state publishing. The
// concrete adapter is wired in main.go to avoid coupling the MQTT
// package to the API server or agent loop.
type StatsSource interface {
	// Uptime returns the process uptime.
	Uptime() time.Duration
	// Version returns the software version string.
	Version() string
	// DefaultModel returns the configured default LLM model name.
	DefaultModel() string
	// LastRequestTime returns when the most recent LLM request completed.
	LastRequestTime() time.Time
}

// DynamicSensor defines a sensor that is registered at runtime and
// published via MQTT discovery alongside the built-in static sensors.
// External packages create DynamicSensor values and register them with
// [Publisher.RegisterSensors].
type DynamicSensor struct {
	// EntitySuffix is the unique suffix used in topic paths and
	// entity IDs (e.g., "nugget_ap" produces state topic
	// thane/{device}/nugget_ap/state).
	EntitySuffix string

	// Config is the HA MQTT discovery payload for this sensor.
	Config SensorConfig
}

// Publisher manages the MQTT connection, publishes HA discovery config
// messages on (re-)connect, subscribes to configured topics, and runs
// a periodic loop that pushes sensor state updates to the broker.
type Publisher struct {
	cfg            config.MQTTConfig
	instanceID     string
	device         DeviceInfo
	origin         OriginInfo
	tokens         *DailyTokens
	stats          StatsSource
	logger         *slog.Logger
	cm             *autopaho.ConnectionManager
	handler        MessageHandler
	rateLimiter    *messageRateLimiter
	mu             sync.Mutex
	dynamicSensors []DynamicSensor
	dynamicTopics  func() []string // returns extra topics to subscribe on (re-)connect

	// migrated tracks entity suffixes whose legacy per-component
	// discovery topic has been migrated (marker published, then
	// cleared) this process. A suffix is recorded only after its clear
	// publish succeeded, so a failed pass retries on the next
	// (re-)connect.
	migrated map[string]bool
}

// New creates a Publisher but does not connect. Call [Publisher.Start]
// to begin the connection and publish loop. A nil logger is replaced
// with [slog.Default]; nil tokens or stats will cause Start to return
// an error.
func New(cfg config.MQTTConfig, instanceID string, tokens *DailyTokens, stats StatsSource, logger *slog.Logger) *Publisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{
		cfg:        cfg,
		instanceID: instanceID,
		device:     NewDeviceInfo(instanceID, cfg.DeviceName),
		origin:     NewOriginInfo(),
		tokens:     tokens,
		stats:      stats,
		logger:     logger,
		migrated:   make(map[string]bool),
	}
}

// SetMessageHandler registers a callback for inbound MQTT messages
// received on subscribed topics. Must be called before [Publisher.Start].
// If not called, a default handler that logs messages at debug level
// is used when subscriptions are configured.
func (p *Publisher) SetMessageHandler(h MessageHandler) {
	p.handler = h
}

// SetDynamicTopics registers a callback that returns additional topic
// filters to include in every (re-)subscribe. The callback is invoked
// on each broker reconnect alongside the static config subscriptions.
// Must be called before [Publisher.Connect].
func (p *Publisher) SetDynamicTopics(fn func() []string) {
	p.dynamicTopics = fn
}

// SubscribeTopics sends a SUBSCRIBE packet for the given topic filters
// on the live broker connection. Safe for concurrent use. Returns an
// error if the publisher is not connected.
func (p *Publisher) SubscribeTopics(ctx context.Context, topics []string) error {
	if len(topics) == 0 {
		return nil
	}
	cm := p.getCM()
	if cm == nil {
		return fmt.Errorf("mqtt publisher not connected")
	}

	opts := make([]paho.SubscribeOptions, len(topics))
	for i, t := range topics {
		opts[i] = paho.SubscribeOptions{Topic: t, QoS: 0}
	}

	if _, err := cm.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: opts,
	}); err != nil {
		return fmt.Errorf("mqtt subscribe: %w", err)
	}

	p.logger.Info("mqtt subscribed to dynamic topics", "topics", topics)
	return nil
}

// RegisterSensors adds dynamic sensor definitions that are published
// via MQTT discovery alongside the built-in static sensors. Must be
// called before [Publisher.Start]. Calling after Start has no effect on
// already-published discovery messages until the next reconnect.
func (p *Publisher) RegisterSensors(sensors []DynamicSensor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dynamicSensors = append(p.dynamicSensors, sensors...)
}

// EnsureSensor registers one dynamic sensor and publishes its discovery
// config immediately, so an entity created after [Publisher.Start] shows
// up in Home Assistant without waiting for the next reconnect. This is
// the runtime counterpart to [Publisher.RegisterSensors], which only
// takes effect at connect time.
//
// Re-registering an existing EntitySuffix replaces that definition
// instead of appending a duplicate, so callers that cannot cheaply track
// whether they have registered yet may call this before every publish.
// It returns an error when the broker connection is not up; the
// registration is still recorded and will be published on the next
// reconnect.
func (p *Publisher) EnsureSensor(ctx context.Context, sensor DynamicSensor) error {
	if strings.TrimSpace(sensor.EntitySuffix) == "" {
		return fmt.Errorf("mqtt: sensor entity suffix is required")
	}

	p.mu.Lock()
	replaced := false
	for i := range p.dynamicSensors {
		if p.dynamicSensors[i].EntitySuffix == sensor.EntitySuffix {
			p.dynamicSensors[i] = sensor
			replaced = true
			break
		}
	}
	if !replaced {
		p.dynamicSensors = append(p.dynamicSensors, sensor)
	}
	p.mu.Unlock()

	cm := p.getCM()
	if cm == nil {
		return fmt.Errorf("mqtt publisher not connected")
	}
	// Device-based discovery describes the whole device in one payload,
	// so a new component means republishing it — which also runs the
	// legacy-topic migration for this suffix if an older binary ever
	// published it per-component.
	p.publishDiscovery(ctx, cm)
	return nil
}

// PublishDynamicState publishes the state and optional JSON attributes
// for a dynamically registered sensor entity. Safe for concurrent use
// from any goroutine.
func (p *Publisher) PublishDynamicState(ctx context.Context, entitySuffix, state string, attrJSON []byte) error {
	cm := p.getCM()
	if cm == nil {
		return fmt.Errorf("mqtt publisher not started")
	}

	if _, err := cm.Publish(ctx, &paho.Publish{
		Topic:   p.StateTopic(entitySuffix),
		Payload: []byte(state),
		QoS:     0,
		Retain:  true,
	}); err != nil {
		return fmt.Errorf("publish state for %s: %w", entitySuffix, err)
	}

	if len(attrJSON) > 0 {
		if _, err := cm.Publish(ctx, &paho.Publish{
			Topic:   p.AttributesTopic(entitySuffix),
			Payload: attrJSON,
			QoS:     0,
			Retain:  true,
		}); err != nil {
			return fmt.Errorf("publish attributes for %s: %w", entitySuffix, err)
		}
	}

	return nil
}

// Connect establishes the MQTT broker connection, publishes discovery
// configs, and configures subscriptions. It does not start the periodic
// publish loop — use [Publisher.PublishStates] in a loop infrastructure
// handler for that.
//
// ctx is the lifecycle context for the MQTT connection manager — it
// must remain valid for as long as the connection should stay alive.
// Do NOT pass a short-lived or timeout-bounded context here; the
// initial connection await uses its own internal timeout.
func (p *Publisher) Connect(ctx context.Context) error {
	return p.connect(ctx)
}

// PublishStates publishes the current sensor state values to the MQTT
// broker. Exported for use by loop infrastructure callers that manage
// their own publish schedule.
func (p *Publisher) PublishStates(ctx context.Context) {
	p.publishStates(ctx)
}

// PublishInterval returns the configured publish interval. Non-positive
// values are replaced with a 5-second minimum. Exported so callers can
// configure loop sleep durations to match.
func (p *Publisher) PublishInterval() time.Duration {
	const minInterval = 5 * time.Second
	interval := time.Duration(p.cfg.PublishIntervalSec) * time.Second
	if interval <= 0 {
		interval = minInterval
	}
	return interval
}

// buildClientConfig constructs the autopaho.ClientConfig for the broker
// connection. It is called by [connect] and is separated for testability
// — callers can inspect the returned config (e.g. OnPublishReceived
// handlers) without needing a live broker.
func (p *Publisher) buildClientConfig(brokerURL *url.URL) autopaho.ClientConfig {
	availTopic := p.AvailabilityTopic()

	pahoCfg := autopaho.ClientConfig{
		ServerUrls:      []*url.URL{brokerURL},
		KeepAlive:       30,
		ConnectUsername: p.cfg.Username,
		ConnectPassword: []byte(p.cfg.Password),
		WillMessage: &paho.WillMessage{
			Topic:   availTopic,
			Payload: []byte("offline"),
			QoS:     1,
			Retain:  true,
		},
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			p.logger.Info("mqtt connected to broker", "broker", p.cfg.Broker)
			publishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			p.publishDiscovery(publishCtx, cm)
			p.publishAvailability(publishCtx, cm, "online")
			p.subscribe(publishCtx, cm)
		},
		OnConnectError: func(err error) {
			p.logger.Warn("mqtt connection error", "error", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: "thane-" + p.instanceID[:8],
		},
	}

	// Enable TLS for mqtts:// or ssl:// schemes.
	if brokerURL.Scheme == "mqtts" || brokerURL.Scheme == "ssl" {
		pahoCfg.TlsCfg = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	// Wire inbound message handler into the paho.ClientConfig so it is
	// baked in BEFORE NewConnection. autopaho copies the config on every
	// (re-)connect, so handlers registered here persist across reconnects.
	// In contrast, cm.AddOnPublishReceived() only registers on the
	// *current* paho.Client instance and is lost on reconnect — and if
	// the connection isn't up yet (c.cli == nil) it silently no-ops.
	hasSubs := len(p.cfg.Subscriptions) > 0 || p.dynamicTopics != nil
	if hasSubs {
		if p.handler == nil {
			p.handler = defaultMessageHandler(p.logger)
		}
		p.rateLimiter = newMessageRateLimiter(100, time.Second, p.logger)

		pahoCfg.OnPublishReceived = append(
			pahoCfg.OnPublishReceived,
			func(pr paho.PublishReceived) (bool, error) {
				if !p.rateLimiter.allow() {
					return true, nil
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							p.logger.Error("mqtt message handler panicked",
								"topic", pr.Packet.Topic,
								"panic", r,
							)
						}
					}()
					p.handler(pr.Packet.Topic, pr.Packet.Payload)
				}()
				return true, nil
			},
		)
	}

	return pahoCfg
}

// connect establishes the MQTT broker connection, publishes discovery
// configs, configures subscriptions, and waits for initial connection.
// Shared by both [Connect] and [Start].
func (p *Publisher) connect(ctx context.Context) error {
	if p.tokens == nil {
		return fmt.Errorf("mqtt publisher: tokens must not be nil")
	}
	if p.stats == nil {
		return fmt.Errorf("mqtt publisher: stats must not be nil")
	}

	brokerURL, err := url.Parse(p.cfg.Broker)
	if err != nil {
		return fmt.Errorf("parse mqtt broker URL: %w", err)
	}

	pahoCfg := p.buildClientConfig(brokerURL)

	cm, err := autopaho.NewConnection(ctx, pahoCfg)
	if err != nil {
		return fmt.Errorf("mqtt connect: %w", err)
	}
	p.setCM(cm)

	// Start the rate limiter after NewConnection succeeds to avoid
	// leaking a goroutine on the error path.
	if p.rateLimiter != nil {
		go p.rateLimiter.start(ctx)
	}

	// Wait for the initial connection before starting the publish loop.
	connCtx, connCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connCancel()
	if err := cm.AwaitConnection(connCtx); err != nil {
		// Log but don't fail — autopaho will keep retrying in the background.
		p.logger.Warn("mqtt initial connection timed out, will retry in background", "error", err)
	}

	return nil
}

// Start connects to the MQTT broker and begins the periodic publish
// loop. It blocks until ctx is cancelled. On every (re-)connect it
// publishes discovery configs, a birth message, and re-subscribes to
// configured topics.
func (p *Publisher) Start(ctx context.Context) error {
	if err := p.connect(ctx); err != nil {
		return err
	}
	p.runLoop(ctx)
	return nil
}

// Stop gracefully disconnects by publishing an "offline" availability
// message before closing the MQTT connection. The provided context
// controls how long to wait for the publish and disconnect to complete.
func (p *Publisher) Stop(ctx context.Context) error {
	cm := p.getCM()
	if cm == nil {
		return nil
	}
	p.publishAvailability(ctx, cm, "offline")
	return cm.Disconnect(ctx)
}

// AwaitConnection blocks until the MQTT broker connection is
// established or ctx expires. Useful for connwatch health probes.
func (p *Publisher) AwaitConnection(ctx context.Context) error {
	cm := p.getCM()
	if cm == nil {
		return fmt.Errorf("mqtt publisher not started")
	}
	return cm.AwaitConnection(ctx)
}

// getCM returns the connection manager under the mutex, safe for
// concurrent reads from PublishDynamicState while Start initializes.
func (p *Publisher) getCM() *autopaho.ConnectionManager {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cm
}

// setCM stores the connection manager under the mutex.
func (p *Publisher) setCM(cm *autopaho.ConnectionManager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cm = cm
}

// ObjectIDPrefix returns the device name normalized for use as an HA
// object_id prefix (hyphens replaced with underscores, trailing
// underscore included). HA uses object_id directly as the entity_id,
// so this prefix ensures entities like sensor.aimee_thane_uptime
// instead of sensor.uptime.
func (p *Publisher) ObjectIDPrefix() string {
	return strings.ReplaceAll(p.cfg.DeviceName, "-", "_") + "_"
}

// --- Topic helpers ---

func (p *Publisher) baseTopic() string {
	return "thane/" + p.cfg.DeviceName
}

// AvailabilityTopic returns the MQTT availability topic for this
// publisher's device. Useful for callers building [DynamicSensor]
// configs that need to reference the shared availability topic.
func (p *Publisher) AvailabilityTopic() string {
	return p.baseTopic() + "/availability"
}

// StateTopic returns the MQTT state topic for the given entity suffix.
// Useful for callers building [DynamicSensor] configs.
func (p *Publisher) StateTopic(entity string) string {
	return p.baseTopic() + "/" + entity + "/state"
}

// AttributesTopic returns the MQTT JSON attributes topic for the given
// entity suffix. Useful for callers building [DynamicSensor] configs.
func (p *Publisher) AttributesTopic(entity string) string {
	return p.baseTopic() + "/" + entity + "/attributes"
}

// deviceDiscoveryTopic is the single retained config topic for HA's
// device-based discovery: one payload describes the device and every
// component on it.
func (p *Publisher) deviceDiscoveryTopic() string {
	return p.cfg.DiscoveryPrefix + "/device/" + p.cfg.DeviceName + "/config"
}

// legacyDiscoveryTopic is the pre-2024.11 per-component discovery topic
// (one retained config per entity). Kept only for the migration sweep:
// markers and cleanup are published here so previously discovered
// entities move to the device-based payload with their registry
// entries, customizations, and history intact.
func (p *Publisher) legacyDiscoveryTopic(component, entity string) string {
	return p.cfg.DiscoveryPrefix + "/" + component + "/" + p.cfg.DeviceName + "/" + entity + "/config"
}

// --- Discovery ---

type sensorDef struct {
	entitySuffix string
	config       SensorConfig
}

func (p *Publisher) sensorDefinitions() []sensorDef {
	prefix := p.ObjectIDPrefix()
	return []sensorDef{
		{
			entitySuffix: "uptime",
			config: SensorConfig{
				Name:           "Uptime",
				ObjectID:       prefix + "uptime",
				HasEntityName:  true,
				UniqueID:       p.instanceID + "_uptime",
				StateTopic:     p.StateTopic("uptime"),
				Icon:           "mdi:clock-outline",
				EntityCategory: "diagnostic",
			},
		},
		{
			entitySuffix: "version",
			config: SensorConfig{
				Name:           "Version",
				ObjectID:       prefix + "version",
				HasEntityName:  true,
				UniqueID:       p.instanceID + "_version",
				StateTopic:     p.StateTopic("version"),
				Icon:           "mdi:tag",
				EntityCategory: "diagnostic",
			},
		},
		{
			entitySuffix: "tokens_today",
			config: SensorConfig{
				Name:              "Tokens Today",
				ObjectID:          prefix + "tokens_today",
				HasEntityName:     true,
				UniqueID:          p.instanceID + "_tokens_today",
				StateTopic:        p.StateTopic("tokens_today"),
				Icon:              "mdi:counter",
				StateClass:        "measurement",
				UnitOfMeasurement: "tokens",
			},
		},
		{
			entitySuffix: "last_request",
			config: SensorConfig{
				Name:           "Last Request",
				ObjectID:       prefix + "last_request",
				HasEntityName:  true,
				UniqueID:       p.instanceID + "_last_request",
				StateTopic:     p.StateTopic("last_request"),
				Icon:           "mdi:clock-check",
				EntityCategory: "diagnostic",
			},
		},
		{
			entitySuffix: "default_model",
			config: SensorConfig{
				Name:           "Default Model",
				ObjectID:       prefix + "default_model",
				HasEntityName:  true,
				UniqueID:       p.instanceID + "_default_model",
				StateTopic:     p.StateTopic("default_model"),
				Icon:           "mdi:brain",
				EntityCategory: "diagnostic",
			},
		},
	}
}

// migrateDiscoveryPayload is HA's documented trigger for moving an
// entity from per-component to device-based discovery: published
// (retained) to the legacy config topic, it unloads the discovered
// entity while KEEPING its registry entry, customizations, and history,
// so the device-based payload can reclaim the same unique_id.
var migrateDiscoveryPayload = []byte(`{"migrate_discovery":true}`)

// publishDiscovery publishes the device-based discovery payload and,
// for any entity suffix seen for the first time this process, performs
// HA's documented legacy migration around it: migrate marker to the
// legacy topic, then the device payload, then an empty retained payload
// clearing the legacy topic. The sweep is idempotent — once the legacy
// topics are empty a later pass has nothing to mark — and self-heals
// the rollback case where an older binary re-littered them.
func (p *Publisher) publishDiscovery(ctx context.Context, cm *autopaho.ConnectionManager) {
	components := p.snapshotComponents()
	pending := p.unmigratedSuffixes(components)

	marked := make([]string, 0, len(pending))
	for _, suffix := range pending {
		topic := p.legacyDiscoveryTopic("sensor", suffix)
		if err := p.publishRetained(ctx, cm, topic, migrateDiscoveryPayload); err != nil {
			p.logger.Warn("mqtt legacy discovery migrate marker failed",
				"entity", suffix, "topic", topic, "error", err)
			continue
		}
		marked = append(marked, suffix)
	}
	if len(marked) > 0 {
		p.logger.Info("mqtt legacy discovery migration started",
			"entities", len(marked))
	}

	if err := p.publishDeviceDiscovery(ctx, cm, components); err != nil {
		// Nothing is recorded as migrated: the next (re-)connect
		// re-marks and re-publishes, and HA sits on the retained
		// markers (registry entries intact) until it does.
		return
	}

	cleared := 0
	for _, suffix := range marked {
		topic := p.legacyDiscoveryTopic("sensor", suffix)
		if err := p.publishRetained(ctx, cm, topic, nil); err != nil {
			p.logger.Warn("mqtt legacy discovery cleanup failed",
				"entity", suffix, "topic", topic, "error", err)
			continue
		}
		p.markMigrated(suffix)
		cleared++
	}
	if cleared > 0 {
		p.logger.Info("mqtt legacy discovery migrated to device-based",
			"entities", cleared)
	}
}

// snapshotComponents assembles the full component map — static
// definitions plus dynamic registrations — with Platform defaulted, as
// one device-based discovery payload's Components block.
func (p *Publisher) snapshotComponents() map[string]SensorConfig {
	components := make(map[string]SensorConfig)
	for _, s := range p.sensorDefinitions() {
		components[s.entitySuffix] = s.config
	}
	p.mu.Lock()
	for _, ds := range p.dynamicSensors {
		components[ds.EntitySuffix] = ds.Config
	}
	p.mu.Unlock()
	for suffix, c := range components {
		if c.Platform == "" {
			c.Platform = "sensor"
			components[suffix] = c
		}
	}
	return components
}

// unmigratedSuffixes returns the component suffixes whose legacy
// discovery topic has not yet been migrated this process, sorted for
// deterministic publish order.
func (p *Publisher) unmigratedSuffixes(components map[string]SensorConfig) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	pending := make([]string, 0, len(components))
	for suffix := range components {
		if !p.migrated[suffix] {
			pending = append(pending, suffix)
		}
	}
	sort.Strings(pending)
	return pending
}

// markMigrated records a suffix whose legacy topic was marked and then
// cleared successfully.
func (p *Publisher) markMigrated(suffix string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.migrated[suffix] = true
}

// publishDeviceDiscovery publishes the single retained device-based
// discovery payload describing this device and every component on it.
func (p *Publisher) publishDeviceDiscovery(ctx context.Context, cm *autopaho.ConnectionManager, components map[string]SensorConfig) error {
	cfg := deviceDiscoveryConfig{
		Device:       p.device,
		Origin:       p.origin,
		Availability: []availabilityEntry{{Topic: p.AvailabilityTopic()}},
		Components:   components,
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		p.logger.Error("mqtt marshal device discovery payload", "error", err)
		return err
	}

	topic := p.deviceDiscoveryTopic()
	if err := p.publishRetained(ctx, cm, topic, payload); err != nil {
		p.logger.Warn("mqtt device discovery publish failed",
			"topic", topic, "components", len(components), "error", err)
		return err
	}
	p.logger.Debug("mqtt device discovery published",
		"topic", topic, "components", len(components))
	return nil
}

// publishRetained publishes a retained QoS-1 message; a nil payload
// clears the retained message from the topic.
func (p *Publisher) publishRetained(ctx context.Context, cm *autopaho.ConnectionManager, topic string, payload []byte) error {
	_, err := cm.Publish(ctx, &paho.Publish{
		Topic:   topic,
		Payload: payload,
		QoS:     1,
		Retain:  true,
	})
	return err
}

func (p *Publisher) publishAvailability(ctx context.Context, cm *autopaho.ConnectionManager, status string) {
	if _, err := cm.Publish(ctx, &paho.Publish{
		Topic:   p.AvailabilityTopic(),
		Payload: []byte(status),
		QoS:     1,
		Retain:  true,
	}); err != nil {
		if status == "offline" && isMQTTNoConnectionError(err) {
			p.logger.Debug("mqtt offline availability publish skipped",
				"status", status,
				"error", err,
			)
			return
		}
		p.logger.Warn("mqtt availability publish failed",
			"status", status, "error", err)
	} else {
		p.logger.Info("mqtt availability published", "status", status)
	}
}

func isMQTTNoConnectionError(err error) bool {
	for err != nil {
		if strings.Contains(err.Error(), "no connection available") {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// --- Subscriptions ---

// subscribe sends SUBSCRIBE packets for all configured and dynamic
// topic filters. Called on every (re-)connect because autopaho does
// not automatically resubscribe after reconnection.
func (p *Publisher) subscribe(ctx context.Context, cm *autopaho.ConnectionManager) {
	topics := p.collectSubscribeTopics()
	if len(topics) == 0 {
		return
	}

	opts := make([]paho.SubscribeOptions, len(topics))
	for i, t := range topics {
		opts[i] = paho.SubscribeOptions{Topic: t, QoS: 0}
	}

	if _, err := cm.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: opts,
	}); err != nil {
		p.logger.Error("mqtt subscribe failed",
			"error", err, "topics", topics)
	} else {
		p.logger.Info("mqtt subscribed to topics", "topics", topics)
	}
}

// collectSubscribeTopics merges config-defined and dynamic topic
// filters, deduplicating by topic string. Order is config first, then
// dynamic.
func (p *Publisher) collectSubscribeTopics() []string {
	seen := make(map[string]struct{})
	var topics []string

	for _, sub := range p.cfg.Subscriptions {
		if _, dup := seen[sub.Topic]; dup {
			continue
		}
		seen[sub.Topic] = struct{}{}
		topics = append(topics, sub.Topic)
	}

	if p.dynamicTopics != nil {
		for _, t := range p.dynamicTopics() {
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			topics = append(topics, t)
		}
	}

	return topics
}

// --- Periodic state loop ---

func (p *Publisher) runLoop(ctx context.Context) {
	const minInterval = 5 * time.Second
	interval := time.Duration(p.cfg.PublishIntervalSec) * time.Second
	if interval <= 0 {
		p.logger.Warn("mqtt publish interval non-positive; using minimum",
			"configured_seconds", p.cfg.PublishIntervalSec,
			"minimum", minInterval.String())
		interval = minInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Publish immediately on start.
	p.publishStates(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.publishStates(ctx)
		}
	}
}

func (p *Publisher) publishStates(ctx context.Context) {
	cm := p.getCM()
	if cm == nil {
		return
	}

	states := map[string]string{
		"uptime":        p.stats.Uptime().Truncate(time.Second).String(),
		"version":       p.stats.Version(),
		"default_model": p.stats.DefaultModel(),
	}

	input, output, _ := p.tokens.Snapshot()
	states["tokens_today"] = strconv.FormatInt(input+output, 10)

	lastReq := p.stats.LastRequestTime()
	if !lastReq.IsZero() {
		states["last_request"] = lastReq.Format(time.RFC3339)
	} else {
		states["last_request"] = "never"
	}

	for entity, value := range states {
		if _, err := cm.Publish(ctx, &paho.Publish{
			Topic:   p.StateTopic(entity),
			Payload: []byte(value),
			QoS:     0,
			Retain:  true,
		}); err != nil {
			p.logger.Debug("mqtt state publish failed",
				"entity", entity, "error", err)
		}
	}

	p.logger.Log(ctx, config.LevelTrace, "mqtt sensor states published",
		"entities", len(states))
}
