package commander

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func actuatorCard() ledger.Card {
	return ledger.Card{
		Device: "testpi",
		Actuators: []ledger.ActuatorProfile{
			{
				ID: "hardware:gpio_servo", Type: "hardware", Category: "motor_control",
				Interface: "gpio", Tier: 1,
				Command: "servoctl",
				Args:    []string{"--pin", "7", "--action", "{action}", "--angle", "{param:angle}"},
			},
			{
				ID: "hardware:camera", Type: "hardware", Category: "video",
				Interface: "usb", Tier: 1, // no command: routing advertisement only
			},
		},
	}
}

// §7.1: an actuator with a declared driver resolves to a native plan whose
// argv still holds its placeholders (the run path fills them).
func TestRouteActuatorWithCommand(t *testing.T) {
	r := NewRouter(actuatorCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"hardware:gpio_servo"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if plan.Kind != "native" || plan.ActuatorID != "hardware:gpio_servo" {
		t.Fatalf("plan = %+v, want native actuator plan", plan)
	}
	if plan.Command != "servoctl" {
		t.Fatalf("command = %q", plan.Command)
	}
}

// An actuator without a command stays a routing advertisement: the plan must
// fall through to whatever comes next (agents here — none, so Route errors).
func TestRouteActuatorNoCommandFallsThrough(t *testing.T) {
	r := NewRouter(actuatorCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	_, err := r.Route([]string{"hardware:camera"})
	if err == nil {
		t.Fatal("command-less actuator produced a plan instead of falling through")
	}
}

func TestSubstituteActionSpec(t *testing.T) {
	plan := Plan{Kind: "native", ActuatorID: "hardware:gpio_servo",
		Command: "servoctl",
		Args:    []string{"--action", "{action}", "--angle", "{param:angle}", "--note", "{intent}"}}
	spec := &ledger.ActionSpec{
		TargetActuator: "hardware:gpio_servo", Action: "rotate",
		Parameters: map[string]any{"angle": 90.0},
	}
	if err := SubstituteActionSpec(&plan, spec, "turn the valve"); err != nil {
		t.Fatalf("substitute: %v", err)
	}
	want := []string{"--action", "rotate", "--angle", "90", "--note", "turn the valve"}
	if strings.Join(plan.Args, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %v, want %v", plan.Args, want)
	}
}

func TestSubstituteActionSpecRejectsMissingParam(t *testing.T) {
	plan := Plan{ActuatorID: "x", Command: "drv", Args: []string{"{param:speed}"}}
	err := SubstituteActionSpec(&plan, &ledger.ActionSpec{}, "")
	if err == nil || !strings.Contains(err.Error(), "speed") {
		t.Fatalf("missing param err = %v", err)
	}
}

func TestSubstituteActionSpecRejectsWrongTarget(t *testing.T) {
	plan := Plan{ActuatorID: "hardware:gpio_servo", Command: "drv"}
	err := SubstituteActionSpec(&plan, &ledger.ActionSpec{TargetActuator: "hardware:other"}, "")
	if err == nil {
		t.Fatal("spec for a different actuator substituted into this plan")
	}
}

func TestSubstituteActionSpecNoSpec(t *testing.T) {
	plan := Plan{ActuatorID: "x", Command: "drv", Args: []string{"--intent", "{intent}"}}
	if err := SubstituteActionSpec(&plan, nil, "do the thing"); err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if plan.Args[1] != "do the thing" {
		t.Fatalf("args = %v", plan.Args)
	}
}

func TestParseActionSpec(t *testing.T) {
	spec, err := ParseActionSpec(`{"action_spec":{"target_actuator":"a","action":"go","parameters":{"n":3}}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec == nil || spec.Action != "go" {
		t.Fatalf("spec = %+v", spec)
	}
	spec, err = ParseActionSpec(`{"unrelated":true}`)
	if err != nil || spec != nil {
		t.Fatalf("spec-less parse = %v %v", spec, err)
	}
	if _, err = ParseActionSpec(""); err != nil {
		t.Fatalf("empty spec: %v", err)
	}
}
