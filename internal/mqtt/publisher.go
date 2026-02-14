package mqtt

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
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
	// ActiveSessions returns the count of active conversation sessions.
	ActiveSessions() int
	// LastRequestTime returns when the most recent LLM request completed.
	LastRequestTime() time.Time
}

// Publisher manages the MQTT connection, publishes HA discovery config
// messages on (re-)connect, and runs a periodic loop that pushes
// sensor state updates to the broker.
type Publisher struct {
	cfg        config.MQTTConfig
	instanceID string
	device     DeviceInfo
	tokens     *DailyTokens
	stats      StatsSource
	logger     *slog.Logger
	cm         *autopaho.ConnectionManager
}

// New creates a Publisher but does not connect. Call [Publisher.Start]
// to begin the connection and publish loop.
func New(cfg config.MQTTConfig, instanceID string, tokens *DailyTokens, stats StatsSource, logger *slog.Logger) *Publisher {
	return &Publisher{
		cfg:        cfg,
		instanceID: instanceID,
		device:     NewDeviceInfo(instanceID, cfg.DeviceName),
		tokens:     tokens,
		stats:      stats,
		logger:     logger,
	}
}

// Start connects to the MQTT broker and begins the periodic publish
// loop. It blocks until ctx is cancelled. On every (re-)connect it
// publishes discovery configs and a birth message.
func (p *Publisher) Start(ctx context.Context) error {
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
			p.publishDiscovery(ctx, cm)
			p.publishAvailability(ctx, cm, "online")
		},
		OnConnectError: func(err error) {
			p.logger.Warn("mqtt connection error", "error", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: "thane-" + p.cfg.DeviceName,
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
				Name:              p.device.Name + " Uptime",
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
				Name:              p.device.Name + " Version",
				UniqueID:          p.instanceID + "_version",
				StateTopic:        p.stateTopic("version"),
				AvailabilityTopic: avail,
				Device:            p.device,
				Icon:              "mdi:tag",
				EntityCategory:    "diagnostic",
			},
		},
		{
			entitySuffix: "active_sessions",
			config: SensorConfig{
				Name:              p.device.Name + " Active Sessions",
				UniqueID:          p.instanceID + "_active_sessions",
				StateTopic:        p.stateTopic("active_sessions"),
				AvailabilityTopic: avail,
				Device:            p.device,
				Icon:              "mdi:chat-processing",
				StateClass:        "measurement",
			},
		},
		{
			entitySuffix: "tokens_today",
			config: SensorConfig{
				Name:              p.device.Name + " Tokens Today",
				UniqueID:          p.instanceID + "_tokens_today",
				StateTopic:        p.stateTopic("tokens_today"),
				AvailabilityTopic: avail,
				Device:            p.device,
				Icon:              "mdi:counter",
				StateClass:        "total_increasing",
				UnitOfMeasurement: "tokens",
			},
		},
		{
			entitySuffix: "last_request",
			config: SensorConfig{
				Name:              p.device.Name + " Last Request",
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
				Name:              p.device.Name + " Default Model",
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
	for _, s := range p.sensorDefinitions() {
		topic := p.discoveryTopic("sensor", s.entitySuffix)
		payload, err := json.Marshal(s.config)
		if err != nil {
			p.logger.Error("mqtt marshal discovery payload",
				"entity", s.entitySuffix, "error", err)
			continue
		}

		if _, err := cm.Publish(ctx, &paho.Publish{
			Topic:   topic,
			Payload: payload,
			QoS:     1,
			Retain:  true,
		}); err != nil {
			p.logger.Warn("mqtt discovery publish failed",
				"entity", s.entitySuffix, "topic", topic, "error", err)
		} else {
			p.logger.Debug("mqtt discovery published",
				"entity", s.entitySuffix, "topic", topic)
		}
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

// --- Periodic state loop ---

func (p *Publisher) runLoop(ctx context.Context) {
	interval := time.Duration(p.cfg.PublishIntervalSec) * time.Second
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
		"uptime":          p.stats.Uptime().Truncate(time.Second).String(),
		"version":         p.stats.Version(),
		"active_sessions": strconv.Itoa(p.stats.ActiveSessions()),
		"default_model":   p.stats.DefaultModel(),
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
