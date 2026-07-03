package journald

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type mockSender struct {
	fields []Field
}

func (m *mockSender) Send(fields []Field) error {
	m.fields = append([]Field(nil), fields...)
	return nil
}

func TestHandlerMapsLevelIdentifierAndAppFields(t *testing.T) {
	sender := &mockSender{}
	handler, err := NewHandler(Options{
		Identifier:  "go-monitoring",
		Level:       slog.LevelDebug,
		FieldPrefix: "GOMON",
		Sender:      sender,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	logger := slog.New(handler)
	logger.Warn("collector tick overrun", "plugin", "smart", "cached", true)

	got := fieldMap(sender.fields)
	if got["SYSLOG_IDENTIFIER"] != "go-monitoring" {
		t.Fatalf("identifier = %q", got["SYSLOG_IDENTIFIER"])
	}
	if got["PRIORITY"] != "4" {
		t.Fatalf("priority = %q", got["PRIORITY"])
	}
	if got["MESSAGE"] != "collector tick overrun" {
		t.Fatalf("message = %q", got["MESSAGE"])
	}
	if got["GOMON_PLUGIN"] != "smart" {
		t.Fatalf("plugin field = %q", got["GOMON_PLUGIN"])
	}
	if got["GOMON_CACHED"] != "true" {
		t.Fatalf("cached field = %q", got["GOMON_CACHED"])
	}
}

func TestHandlerDefaultsToUnprefixedAppFields(t *testing.T) {
	sender := &mockSender{}
	handler, err := NewHandler(Options{
		Identifier: "generic-service",
		Level:      slog.LevelDebug,
		Sender:     sender,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	slog.New(handler).Info("request complete", "user", "miguelmariz")

	got := fieldMap(sender.fields)
	if got["USER"] != "miguelmariz" {
		t.Fatalf("USER = %q", got["USER"])
	}
	if _, ok := got["GOMON_USER"]; ok {
		t.Fatalf("GOMON_USER unexpectedly present: %q", got["GOMON_USER"])
	}
}

func TestHandlerAddsSourceFields(t *testing.T) {
	sender := &mockSender{}
	handler, err := NewHandler(Options{
		Identifier: "go-monitoring",
		Level:      slog.LevelDebug,
		AddSource:  true,
		Sender:     sender,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	logWithSource(slog.New(handler))
	got := fieldMap(sender.fields)
	if !strings.HasSuffix(got["CODE_FILE"], "handler_test.go") {
		t.Fatalf("CODE_FILE = %q", got["CODE_FILE"])
	}
	if !strings.Contains(got["CODE_FUNC"], "logWithSource") {
		t.Fatalf("CODE_FUNC = %q", got["CODE_FUNC"])
	}
	if got["CODE_LINE"] == "" {
		t.Fatal("CODE_LINE missing")
	}
}

func TestHandlerFlattensGroupsAndEncodesComplexValues(t *testing.T) {
	sender := &mockSender{}
	handler, err := NewHandler(Options{
		Identifier:  "go-monitoring",
		Level:       slog.LevelDebug,
		FieldPrefix: "GOMON",
		Sender:      sender,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	logger := slog.New(handler).WithGroup("request")
	logger.Info("request finished",
		slog.Group("network", slog.String("interface", "eth0")),
		slog.Any("payload", map[string]any{"status": "ok", "count": 2}),
	)

	got := fieldMap(sender.fields)
	if got["GOMON_REQUEST_NETWORK_INTERFACE"] != "eth0" {
		t.Fatalf("group field = %q", got["GOMON_REQUEST_NETWORK_INTERFACE"])
	}
	if got["GOMON_REQUEST_PAYLOAD"] != `{"count":2,"status":"ok"}` && got["GOMON_REQUEST_PAYLOAD"] != `{"status":"ok","count":2}` {
		t.Fatalf("payload field = %q", got["GOMON_REQUEST_PAYLOAD"])
	}
}

func TestHandlerAllowsStandardFieldPassthroughAndLastWriteWins(t *testing.T) {
	sender := &mockSender{}
	handler, err := NewHandler(Options{
		Identifier:  "go-monitoring",
		Level:       slog.LevelDebug,
		AddSource:   true,
		FieldPrefix: "GOMON",
		Sender:      sender,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	record := slog.NewRecord(testTime, slog.LevelError, "smart scan failed", 0)
	record.AddAttrs(
		slog.String("message_id", "gomon.test"),
		slog.String("code_file", "/override/file.go"),
		slog.String("user", "first"),
		slog.String("user", "second"),
	)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := fieldMap(sender.fields)
	if got["MESSAGE_ID"] != "gomon.test" {
		t.Fatalf("MESSAGE_ID = %q", got["MESSAGE_ID"])
	}
	if got["CODE_FILE"] != "/override/file.go" {
		t.Fatalf("CODE_FILE = %q", got["CODE_FILE"])
	}
	if got["GOMON_USER"] != "second" {
		t.Fatalf("GOMON_USER = %q", got["GOMON_USER"])
	}
}

func TestHandlerFieldNamesAreUnprefixedAtCallSite(t *testing.T) {
	sender := &mockSender{}
	handler, err := NewHandler(Options{
		Identifier:  "go-monitoring",
		Level:       slog.LevelDebug,
		FieldPrefix: "GOMON",
		Sender:      sender,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	slog.New(handler).Info("agent boot",
		"effective_uid", 0,
		"uid", 1000,
		"gid", 1000,
	)

	got := fieldMap(sender.fields)
	if got["GOMON_EFFECTIVE_UID"] != "0" {
		t.Fatalf("GOMON_EFFECTIVE_UID = %q", got["GOMON_EFFECTIVE_UID"])
	}
	if got["GOMON_UID"] != "1000" {
		t.Fatalf("GOMON_UID = %q", got["GOMON_UID"])
	}
	if got["GOMON_GID"] != "1000" {
		t.Fatalf("GOMON_GID = %q", got["GOMON_GID"])
	}
	if _, ok := got["GOMON_GOMON_UID"]; ok {
		t.Fatalf("GOMON_GOMON_UID unexpectedly present: %q", got["GOMON_GOMON_UID"])
	}
	if _, ok := got["GOMON_GOMON_GID"]; ok {
		t.Fatalf("GOMON_GOMON_GID unexpectedly present: %q", got["GOMON_GOMON_GID"])
	}
}

var testTime = time.Unix(1_700_000_000, 0)

func logWithSource(logger *slog.Logger) {
	logger.Info("with source")
}

func fieldMap(fields []Field) map[string]string {
	out := make(map[string]string, len(fields))
	for _, field := range fields {
		out[field.Name] = field.Value
	}
	return out
}
