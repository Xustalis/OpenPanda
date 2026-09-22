package ledger

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestActuatorProfileParsingAndMatching(t *testing.T) {
	yamlData := `
device: "orange-pi-zero3"
chip: "allwinner-h618"
node_kind: "physical"

actuators:
  - id: "agent:claude_code"
    type: "software"
    category: "coding"
    cost_tier: "high"
    tier: 2
    capabilities: ["refactor", "code_review"]

  - id: "hardware:gpio_servo"
    type: "hardware"
    category: "motor_control"
    interface: "gpio"
    pin_mapping: [12, 16]
    capabilities: ["rotate_0_180", "speed_control"]
    tier: 1

  - id: "hardware:mic_voice_input"
    type: "hardware"
    category: "audio_sensing"
    interface: "usb_audio"
    capabilities: ["record_stream", "wake_word_detect"]
    tier: 1
`
	var card Card
	if err := yaml.Unmarshal([]byte(yamlData), &card); err != nil {
		t.Fatalf("unmarshal yaml with actuators: %v", err)
	}

	if len(card.Actuators) != 3 {
		t.Fatalf("expected 3 actuators, got %d", len(card.Actuators))
	}

	servo := card.Actuators[1]
	if servo.ID != "hardware:gpio_servo" || servo.Type != "hardware" || servo.Category != "motor_control" {
		t.Fatalf("unexpected servo actuator: %+v", servo)
	}
	if len(servo.PinMapping) != 2 || servo.PinMapping[0] != 12 {
		t.Fatalf("unexpected pin mapping: %+v", servo.PinMapping)
	}

	node := Node{
		ID:        "pi-zero",
		Actuators: card.Actuators,
	}

	// Verify Abilities list includes actuators
	abilities := node.Abilities()
	if len(abilities) != 3 {
		t.Fatalf("expected 3 abilities, got %d: %v", len(abilities), abilities)
	}

	// Verify Matches with actuator ID
	if !node.Matches([]string{"hardware:gpio_servo"}) {
		t.Fatalf("expected node to match hardware:gpio_servo")
	}

	// Verify Matches with actuator capability
	if !node.Matches([]string{"rotate_0_180"}) {
		t.Fatalf("expected node to match capability rotate_0_180")
	}
	if !node.Matches([]string{"wake_word_detect"}) {
		t.Fatalf("expected node to match capability wake_word_detect")
	}

	// Verify non-matching ability returns false
	if node.Matches([]string{"gpu:train"}) {
		t.Fatalf("expected node NOT to match gpu:train")
	}
}

func TestActionSpec(t *testing.T) {
	spec := ActionSpec{
		TargetActuator: "hardware:gpio_servo",
		Action:         "rotate",
		Parameters: map[string]any{
			"angle": 90,
			"speed": 5,
		},
	}
	if spec.TargetActuator != "hardware:gpio_servo" || spec.Action != "rotate" {
		t.Fatalf("unexpected ActionSpec: %+v", spec)
	}
	if spec.Parameters["angle"] != 90 {
		t.Fatalf("unexpected parameter: %v", spec.Parameters["angle"])
	}
}
