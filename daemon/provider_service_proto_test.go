package daemon

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestProviderServiceDescriptor(t *testing.T) {
	service := File_daemon_provider_service_proto.Services().ByName("ProviderService")
	if service == nil {
		t.Fatal("ProviderService descriptor is missing")
	}
	expectedMethods := []struct {
		name            protoreflect.Name
		serverStreaming bool
	}{
		{"GetServiceInfo", false},
		{"ListProviders", false},
		{"GetProvider", false},
		{"SubscribeProviders", true},
		{"RefreshProvider", false},
		{"HealthCheckProvider", false},
	}
	if service.Methods().Len() != len(expectedMethods) {
		t.Fatalf("unexpected method count: %d", service.Methods().Len())
	}
	for index, expected := range expectedMethods {
		method := service.Methods().Get(index)
		if method.Name() != expected.name || method.IsStreamingServer() != expected.serverStreaming || method.IsStreamingClient() {
			t.Fatalf("unexpected method %d: %s client_stream=%v server_stream=%v", index, method.Name(), method.IsStreamingClient(), method.IsStreamingServer())
		}
	}

	assertFieldNumber(t, "ProviderList", "instance_id", 1)
	assertFieldNumber(t, "ProviderList", "revision", 2)
	assertFieldNumber(t, "ProviderList", "providers", 3)
	assertFieldNumber(t, "Provider", "tag", 1)
	assertFieldNumber(t, "Provider", "type", 2)
	assertFieldNumber(t, "Provider", "updated_at_ms", 3)
	assertFieldNumber(t, "ProviderActionResult", "revision", 2)
	assertFieldNumber(t, "ProviderHealthCheckResult", "results", 4)
	forbiddenNames := []string{"url", "header", "token", "secret", "path", "cache", "config"}
	messages := File_daemon_provider_service_proto.Messages()
	for messageIndex := 0; messageIndex < messages.Len(); messageIndex++ {
		fields := messages.Get(messageIndex).Fields()
		for fieldIndex := 0; fieldIndex < fields.Len(); fieldIndex++ {
			fieldName := strings.ToLower(string(fields.Get(fieldIndex).Name()))
			for _, forbiddenName := range forbiddenNames {
				if strings.Contains(fieldName, forbiddenName) {
					t.Fatalf("sensitive field exposed in ProviderService schema: %s.%s", messages.Get(messageIndex).Name(), fieldName)
				}
			}
		}
	}

	if APIVersion != 3 {
		t.Fatalf("ProviderService must not change StartedService APIVersion: %d", APIVersion)
	}
}

func assertFieldNumber(t *testing.T, messageName protoreflect.Name, fieldName protoreflect.Name, number protoreflect.FieldNumber) {
	t.Helper()
	message := File_daemon_provider_service_proto.Messages().ByName(messageName)
	if message == nil {
		t.Fatalf("message %s is missing", messageName)
	}
	field := message.Fields().ByName(fieldName)
	if field == nil {
		t.Fatalf("field %s.%s is missing", messageName, fieldName)
	}
	if field.Number() != number {
		t.Fatalf("unexpected field number for %s.%s: %d", messageName, fieldName, field.Number())
	}
}
