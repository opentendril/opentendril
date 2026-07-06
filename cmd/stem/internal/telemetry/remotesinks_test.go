package telemetry

import (
	"testing"
)

func TestParseRemoteSinkRedis(t *testing.T) {
	transporter, err := ParseRemoteSink("redis://:secret@localhost:6380/fleet-events")
	if err != nil {
		t.Fatalf("ParseRemoteSink: %v", err)
	}
	redis, ok := transporter.(*RedisTransporter)
	if !ok {
		t.Fatalf("expected RedisTransporter, got %T", transporter)
	}
	if redis.addr != "localhost:6380" || redis.channel != "fleet-events" || redis.password != "secret" {
		t.Fatalf("unexpected redis config: %+v", redis)
	}
}

func TestParseRemoteSinkRedisDefaults(t *testing.T) {
	transporter, err := ParseRemoteSink("redis://localhost")
	if err != nil {
		t.Fatalf("ParseRemoteSink: %v", err)
	}
	redis := transporter.(*RedisTransporter)
	if redis.addr != "localhost:6379" {
		t.Fatalf("expected default port, got %q", redis.addr)
	}
	if redis.channel != defaultRedisChannel {
		t.Fatalf("expected default channel, got %q", redis.channel)
	}
}

func TestParseRemoteSinkWebSocket(t *testing.T) {
	transporter, err := ParseRemoteSink("wss://token@fleet.example.com/ingest")
	if err != nil {
		t.Fatalf("ParseRemoteSink: %v", err)
	}
	ws, ok := transporter.(*RemoteWebSocketTransporter)
	if !ok {
		t.Fatalf("expected RemoteWebSocketTransporter, got %T", transporter)
	}
	if ws.endpoint != "wss://fleet.example.com/ingest" {
		t.Fatalf("expected credentials stripped from endpoint, got %q", ws.endpoint)
	}
	if ws.apiKey != "token" {
		t.Fatalf("expected apiKey from userinfo, got %q", ws.apiKey)
	}
}

func TestParseRemoteSinkWebhook(t *testing.T) {
	transporter, err := ParseRemoteSink("https://hooks.example.com/telemetry")
	if err != nil {
		t.Fatalf("ParseRemoteSink: %v", err)
	}
	if _, ok := transporter.(*WebhookTransporter); !ok {
		t.Fatalf("expected WebhookTransporter, got %T", transporter)
	}
}

func TestParseRemoteSinkRejectsUnknownScheme(t *testing.T) {
	if _, err := ParseRemoteSink("ftp://example.com"); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}

func TestTransportersFromEnv(t *testing.T) {
	t.Setenv(EnvRemoteSinks, "redis://localhost/one, wss://fleet.example.com/ingest, ftp://bad")
	transporters, errs := TransportersFromEnv()
	if len(transporters) != 2 {
		t.Fatalf("expected 2 transporters, got %d", len(transporters))
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error for the ftp sink, got %d", len(errs))
	}

	t.Setenv(EnvRemoteSinks, "")
	transporters, errs = TransportersFromEnv()
	if transporters != nil || errs != nil {
		t.Fatal("expected empty env to yield no transporters")
	}
}
