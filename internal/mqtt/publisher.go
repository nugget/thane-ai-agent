package mqtt

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nugget/thane-ai-agent/internal/config"
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
	tokens         *DailyTokens
	stats          StatsSource
	logger         *slog.Logger
	cm             *autopaho.ConnectionManager
	handler        MessageHandler
	rateLimiter    *messageRateLimiter
	mu             sync.Mutex
	dynamicSensors []DynamicSensor
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
		tokens:     tokens,
		stats:      stats,
		logger:     logger,
	}
}

// SetMessageHandler registers a callback for inbound MQTT messages
// received on subscribed topics. Must be called before [Publisher.Start].
// If not called, a default handler that logs messages at debug level
// is used when subscriptions are configured.
func (p *Publisher) SetMessageHandler(h MessageHandler) {
	p.handler = h
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

// Device returns the HA device info shared across all sensors published
// by this publisher instance. Useful for callers building [DynamicSensor]
// configs that reference the same HA device.
func (p *Publisher) Device() DeviceInfo {
	return p.device
}

// PublishDynamicState publishes the state and optional JSON attributes
// for a dynamically registered sensor entity. Safe for concurrent use
// from any goroutine.
func (p *Publisher) PublishDynamicState(ctx context.Context, entitySuffix, state string, attrJSON []byte) error {
	if p.cm == nil {
		return fmt.Errorf("mqtt publisher not started")
	}

	if _, err := p.cm.Publish(ctx, &paho.Publish{
		Topic:   p.stateTopic(entitySuffix),
		Payload: []byte(state),
		QoS:     0,
		Retain:  true,
	}); err != nil {
		return fmt.Errorf("publish state for %s: %w", entitySuffix, err)
	}

	if len(attrJSON) > 0 {
		if _, err := p.cm.Publish(ctx, &paho.Publish{
			Topic:   p.attributesTopic(entitySuffix),
			Payload: attrJSON,
			QoS:     0,
			Retain:  true,
		}); err != nil {
			return fmt.Errorf("publish attributes for %s: %w", entitySuffix, err)
		}
	}

	return nil
}

// Start connects to the MQTT broker and begins the periodic publish
// loop. It blocks until ctx is cancelled. On every (re-)connect it
// publishes discovery configs, a birth message, and re-subscribes to
// configured topics.
func (p *Publisher) Start(ctx context.Context) error {
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

	availTopic := p.availabilityTopic()

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

	cm, err := autopaho.NewConnection(ctx, pahoCfg)
	if err != nil {
		return fmt.Errorf("mqtt connect: %w", err)
	}
	p.cm = cm

	// Wire inbound message handler if subscriptions are configured.
	if len(p.cfg.Subscriptions) > 0 {
		if p.handler == nil {
			p.handler = defaultMessageHandler(p.logger)
		}
		p.rateLimiter = newMessageRateLimiter(100, time.Second, p.logger)
		go p.rateLimiter.start(ctx)

		cm.AddOnPublishReceived(func(pr autopaho.PublishReceived) (bool, error) {
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
		})
	}

	// Wait for the initial connection before starting the publish loop.
	connCtx, connCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connCancel()
	if err := cm.AwaitConnection(connCtx); err != nil {
		// Log but don't fail — autopaho will keep retrying in the background.
		p.logger.Warn("mqtt initial connection timed out, will retry in background", "error", err)
	}

	// Run the periodic state publish loop until ctx is cancelled.
	p.runLoop(ctx)
	return nil
}

// Stop gracefully disconnects by publishing an "offline" availability
// message before closing the MQTT connection. The provided context
// controls how long to wait for the publish and disconnect to complete.
func (p *Publisher) Stop(ctx context.Context) error {
	if p.cm == nil {
		return nil
	}
	p.publishAvailability(ctx, p.cm, "offline")
	return p.cm.Disconnect(ctx)
}

// AwaitConnection blocks until the MQTT broker connection is
// established or ctx expires. Useful for connwatch health probes.
func (p *Publisher) AwaitConnection(ctx context.Context) error {
	if p.cm == nil {
		return fmt.Errorf("mqtt publisher not started")
	}
	return p.cm.AwaitConnection(ctx)
}

// --- Topic helpers ---

func (p *Publisher) baseTopic() string {
	return "thane/" + p.cfg.DeviceName
}

func (p *Publisher) availabilityTopic() string {
	return p.baseTopic() + "/availability"
}

func (p *Publisher) stateTopic(entity string) string {
	return p.baseTopic() + "/" + entity + "/state"
}

func (p *Publisher) attributesTopic(entity string) string {
	return p.baseTopic() + "/" + entity + "/attributes"
}

func (p *Publisher) discoveryTopic(component, entity string) string {
	return p.cfg.DiscoveryPrefix + "/" + component + "/" + p.cfg.DeviceName + "/" + entity + "/config"
}

// --- Discovery ---

type sensorDef struct {
	entitySuffix string
	config       SensorConfig
}

func (p *Publisher) sensorDefinitions() []sensorDef {
	avail := p.availabilityTopic()
	return []sensorDef{
		{
			entitySuffix: "uptime",
			config: SensorConfig{
				Name:              "Uptime",
				ObjectID:          "uptime",
				HasEntityName:     true,
				UniqueID:          p.instanceID + "_uptime",
				StateTopic:        p.stateTopic("uptime"),
				AvailabilityTopic: avail,
				Device:            p.device,
				Icon:              "mdi:clock-outline",
				EntityCategory:    "diagnostic",
			},
		},
		{
			entitySuffix: "version",
			config: SensorConfig{
				Name:              "Version",
				ObjectID:          "version",
				HasEntityName:     true,
				UniqueID:          p.instanceID + "_version",
				StateTopic:        p.stateTopic("version"),
				AvailabilityTopic: avail,
				Device:            p.device,
				Icon:              "mdi:tag",
				EntityCategory:    "diagnostic",
			},
		},
		{
			entitySuffix: "tokens_today",
			config: SensorConfig{
				Name:              "Tokens Today",
				ObjectID:          "tokens_today",
				HasEntityName:     true,
				UniqueID:          p.instanceID + "_tokens_today",
				StateTopic:        p.stateTopic("tokens_today"),
				AvailabilityTopic: avail,
				Device:            p.device,
				Icon:              "mdi:counter",
				StateClass:        "measurement",
				UnitOfMeasurement: "tokens",
			},
		},
		{
			entitySuffix: "last_request",
			config: SensorConfig{
				Name:              "Last Request",
				ObjectID:          "last_request",
				HasEntityName:     true,
				UniqueID:          p.instanceID + "_last_request",
				StateTopic:        p.stateTopic("last_request"),
				AvailabilityTopic: avail,
				Device:            p.device,
				Icon:              "mdi:clock-check",
				EntityCategory:    "diagnostic",
			},
		},
		{
			entitySuffix: "default_model",
			config: SensorConfig{
				Name:              "Default Model",
				ObjectID:          "default_model",
				HasEntityName:     true,
				UniqueID:          p.instanceID + "_default_model",
				StateTopic:        p.stateTopic("default_model"),
				AvailabilityTopic: avail,
				Device:            p.device,
				Icon:              "mdi:brain",
				EntityCategory:    "diagnostic",
			},
		},
	}
}

func (p *Publisher) publishDiscovery(ctx context.Context, cm *autopaho.ConnectionManager) {
	// Static (built-in) sensors.
	for _, s := range p.sensorDefinitions() {
		p.publishSensorDiscovery(ctx, cm, s.entitySuffix, s.config)
	}

	// Dynamic sensors registered by external packages.
	p.mu.Lock()
	dynCopy := make([]DynamicSensor, len(p.dynamicSensors))
	copy(dynCopy, p.dynamicSensors)
	p.mu.Unlock()

	for _, ds := range dynCopy {
		p.publishSensorDiscovery(ctx, cm, ds.EntitySuffix, ds.Config)
	}
}

func (p *Publisher) publishSensorDiscovery(ctx context.Context, cm *autopaho.ConnectionManager, entitySuffix string, cfg SensorConfig) {
	topic := p.discoveryTopic("sensor", entitySuffix)
	payload, err := json.Marshal(cfg)
	if err != nil {
		p.logger.Error("mqtt marshal discovery payload",
			"entity", entitySuffix, "error", err)
		return
	}

	if _, err := cm.Publish(ctx, &paho.Publish{
		Topic:   topic,
		Payload: payload,
		QoS:     1,
		Retain:  true,
	}); err != nil {
		p.logger.Warn("mqtt discovery publish failed",
			"entity", entitySuffix, "topic", topic, "error", err)
	} else {
		p.logger.Debug("mqtt discovery published",
			"entity", entitySuffix, "topic", topic)
	}
}

func (p *Publisher) publishAvailability(ctx context.Context, cm *autopaho.ConnectionManager, status string) {
	if _, err := cm.Publish(ctx, &paho.Publish{
		Topic:   p.availabilityTopic(),
		Payload: []byte(status),
		QoS:     1,
		Retain:  true,
	}); err != nil {
		p.logger.Warn("mqtt availability publish failed",
			"status", status, "error", err)
	} else {
		p.logger.Info("mqtt availability published", "status", status)
	}
}

// --- Subscriptions ---

// subscribe sends SUBSCRIBE packets for all configured topic filters.
// Called on every (re-)connect because autopaho does not automatically
// resubscribe after reconnection.
func (p *Publisher) subscribe(ctx context.Context, cm *autopaho.ConnectionManager) {
	if len(p.cfg.Subscriptions) == 0 {
		return
	}

	opts := make([]paho.SubscribeOptions, 0, len(p.cfg.Subscriptions))
	topics := make([]string, 0, len(p.cfg.Subscriptions))
	for _, sub := range p.cfg.Subscriptions {
		opts = append(opts, paho.SubscribeOptions{
			Topic: sub.Topic,
			QoS:   0,
		})
		topics = append(topics, sub.Topic)
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
	if p.cm == nil {
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
		if _, err := p.cm.Publish(ctx, &paho.Publish{
			Topic:   p.stateTopic(entity),
			Payload: []byte(value),
			QoS:     0,
			Retain:  true,
		}); err != nil {
			p.logger.Debug("mqtt state publish failed",
				"entity", entity, "error", err)
		}
	}

	p.logger.Debug("mqtt sensor states published",
		"entities", len(states))
}
