package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func mcpToolNames(t *testing.T, a *App) []string {
	t.Helper()
	resp := a.handleRPC(rpcMsg{ID: json.RawMessage("1"), Method: "tools/list"})
	var result struct{ Tools []tool }
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestLiveMCPExposesOnlyCorePhoneTools(t *testing.T) {
	t.Setenv("ESP32AGENT_REQUIRE_LIVE", "1")
	a := &App{Dir: t.TempDir()}
	names := mcpToolNames(t, a)
	want := []string{"screenshot", "tap", "longpress", "swipe", "scroll", "type_text", "press_key",
		"press_button", "device_info", "restart_stream", "refresh_screen"}
	if !slices.Equal(names, want) {
		t.Fatalf("unexpected live tools: %q", names)
	}
	for _, name := range []string{"history", "intent_list", "intent_show", "intent_save", "memory_recall",
		"memory_add", "phone_audio", "send"} {
		got := a.callTool(name, nil)
		if got["isError"] != true || !strings.Contains(got["content"].([]map[string]any)[0]["text"].(string), "unavailable") {
			t.Errorf("live profile allowed %q: %+v", name, got)
		}
	}
	t.Setenv("ESP32AGENT_REQUIRE_LIVE", "")
	names = mcpToolNames(t, a)
	for _, name := range []string{"history", "intent_list", "memory_recall", "phone_audio", "scroll"} {
		if !slices.Contains(names, name) {
			t.Errorf("standard MCP profile lost %q", name)
		}
	}
}

func TestMCPScrollRejectsInvalidDeltaBeforeDaemon(t *testing.T) {
	t.Setenv("ESP32AGENT_REQUIRE_LIVE", "1")
	a := &App{}
	for _, delta := range []any{nil, "2", 0.0, 1.5, -11.0, 11.0} {
		result := a.callTool("scroll", map[string]any{"delta": delta})
		if result["isError"] != true {
			t.Errorf("MCP accepted invalid delta %v", delta)
		}
	}
}
