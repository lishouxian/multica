package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodePlaybookDefinitionRejectsCyclesAndUnknownFields(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		_, err := DecodePlaybookDefinition([]byte(`{
          "version": 1,
          "steps": [
            {"key":"a","title":"A","agent_id":"11111111-1111-1111-1111-111111111111","depends_on":["b"],"output_schema":{"type":"object","properties":{}}},
            {"key":"b","title":"B","agent_id":"22222222-2222-2222-2222-222222222222","depends_on":["a"],"output_schema":{"type":"object","properties":{}}}
          ]
        }`))
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("expected cycle error, got %v", err)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		_, err := DecodePlaybookDefinition([]byte(`{
          "version": 1,
          "steps": [{"key":"a","title":"A","agent_id":"11111111-1111-1111-1111-111111111111","magic":true,"output_schema":{"type":"object","properties":{}}}]
        }`))
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("expected unknown-field error, got %v", err)
		}
	})
}

func TestDecodePlaybookDefinitionAcceptsBoundedStateMachineCycles(t *testing.T) {
	definition, err := DecodePlaybookDefinition([]byte(`{
      "version": 1,
      "start": "design",
      "max_transitions": 12,
      "steps": [
        {
          "key":"design","title":"Design","agent_id":"11111111-1111-1111-1111-111111111111","max_attempts":4,
          "transitions":[{"to":"code"}],
          "output_schema":{"type":"object","properties":{}}
        },
        {
          "key":"code","title":"Code","agent_id":"22222222-2222-2222-2222-222222222222","max_attempts":6,
          "transitions":[
            {"to":"design","when":{"field":"outcome","equals":"design_issue"}},
            {"end":true}
          ],
          "output_schema":{"type":"object","properties":{"outcome":{"type":"string"}}}
        }
      ]
    }`))
	if err != nil {
		t.Fatalf("state-machine cycle rejected: %v", err)
	}
	if definition.Start != "design" || definition.MaxTransitions != 12 {
		t.Fatalf("decoded state-machine controls = %q/%d", definition.Start, definition.MaxTransitions)
	}
}

func TestDecodePlaybookDefinitionRejectsMixedDAGAndStateMachine(t *testing.T) {
	_, err := DecodePlaybookDefinition([]byte(`{
      "version": 1,
      "start": "a",
      "steps": [
        {"key":"a","title":"A","agent_id":"11111111-1111-1111-1111-111111111111","depends_on":["b"],"transitions":[{"to":"b"}],"output_schema":{"type":"object","properties":{}}},
        {"key":"b","title":"B","agent_id":"22222222-2222-2222-2222-222222222222","transitions":[{"end":true}],"output_schema":{"type":"object","properties":{}}}
      ]
    }`))
	if err == nil || !strings.Contains(err.Error(), "cannot use depends_on") {
		t.Fatalf("expected mixed-mode error, got %v", err)
	}
}

func TestValidateOutputValueEnforcesRequiredEnumAndAdditionalProperties(t *testing.T) {
	additional := false
	schema := ValueSchema{
		Type:     "object",
		Required: []string{"outcome", "files"},
		Properties: map[string]ValueSchema{
			"outcome": {Type: "string", Enum: byteSlices(`"fix"`, `"close"`)},
			"files":   {Type: "array", Items: &ValueSchema{Type: "string"}},
		},
		AdditionalProperties: &additional,
	}
	if err := validateOutputValue(map[string]any{"outcome": "fix", "files": []any{"a.go"}}, schema, "output"); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}
	for _, value := range []map[string]any{
		{"outcome": "unknown", "files": []any{"a.go"}},
		{"outcome": "fix"},
		{"outcome": "fix", "files": []any{"a.go"}, "extra": true},
	} {
		if err := validateOutputValue(value, schema, "output"); err == nil {
			t.Fatalf("invalid output accepted: %#v", value)
		}
	}
}

func byteSlices(values ...string) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		result = append(result, json.RawMessage(value))
	}
	return result
}
