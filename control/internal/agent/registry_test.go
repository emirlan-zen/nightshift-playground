package agent

import "testing"

func TestRegistryIsImmutable(t *testing.T) {
	input := []string{"agent-a", "playground"}
	registry := NewRegistry(input)
	input[0] = "changed"
	if !registry.Contains("agent-a") || registry.Contains("changed") {
		t.Fatal("registry changed with constructor input")
	}
	output := registry.All()
	output[0] = "changed"
	if !registry.Contains("agent-a") {
		t.Fatal("registry changed with returned slice")
	}
}
