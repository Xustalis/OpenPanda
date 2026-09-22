package commander

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// actuator.go implements the §7.2 dispatch half of the unified actuator
// model: a card actuator's command carries {intent}, {action} and
// {param:<name>} placeholders, and the task's action_spec supplies the
// concrete values. Substitution produces argv elements — never a shell line —
// so a hostile parameter value can be a weird argument but not an injection.

// ParseActionSpec extracts the action_spec block from a task's spec JSON.
// A spec without one yields nil, nil — the actuator's {intent} substitution
// still applies, and a command needing {action}/{param:...} then fails
// substitution with a clear error instead of executing literal braces.
func ParseActionSpec(specJSON string) (*ledger.ActionSpec, error) {
	if strings.TrimSpace(specJSON) == "" {
		return nil, nil
	}
	var holder struct {
		ActionSpec *ledger.ActionSpec `json:"action_spec,omitempty"`
	}
	if err := json.Unmarshal([]byte(specJSON), &holder); err != nil {
		return nil, fmt.Errorf("action_spec: %w", err)
	}
	return holder.ActionSpec, nil
}

// SubstituteActionSpec fills an actuator plan's placeholders from spec and
// intent, in place. It is a no-op for non-actuator plans. A spec naming a
// different actuator than the plan resolved is rejected — executing another
// actuator's command with this spec's parameters would act on hardware the
// task never asked for.
func SubstituteActionSpec(p *Plan, spec *ledger.ActionSpec, intent string) error {
	if p == nil || p.ActuatorID == "" {
		return nil
	}
	if spec != nil && spec.TargetActuator != "" && !ledger.AbilityMatches(p.ActuatorID, spec.TargetActuator) {
		return fmt.Errorf("action_spec targets %q but plan resolved actuator %q",
			spec.TargetActuator, p.ActuatorID)
	}
	action := ""
	var params map[string]any
	if spec != nil {
		action = spec.Action
		params = spec.Parameters
	}
	subst := func(arg string) (string, error) {
		out := strings.ReplaceAll(arg, "{intent}", intent)
		out = strings.ReplaceAll(out, "{action}", action)
		for {
			i := strings.Index(out, "{param:")
			if i < 0 {
				break
			}
			j := strings.Index(out[i:], "}")
			if j < 0 {
				return "", fmt.Errorf("unterminated {param:...} placeholder in %q", arg)
			}
			name := out[i+len("{param:") : i+j]
			v, ok := params[name]
			if !ok {
				return "", fmt.Errorf("action_spec provides no parameter %q needed by %q", name, arg)
			}
			s, err := scalarString(v)
			if err != nil {
				return "", fmt.Errorf("parameter %q: %w", name, err)
			}
			out = out[:i] + s + out[i+j+1:]
		}
		return out, nil
	}
	cmd, err := subst(p.Command)
	if err != nil {
		return err
	}
	args := make([]string, len(p.Args))
	for i, a := range p.Args {
		if args[i], err = subst(a); err != nil {
			return err
		}
	}
	p.Command, p.Args = cmd, args
	return nil
}

// scalarString renders a JSON scalar as one argv element. Structured values
// (objects, arrays) are refused rather than stringified: an actuator driver
// expecting `--angle 90` must not silently receive `--angle {"x":1}`.
func scalarString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("non-scalar value %T cannot fill a command placeholder", v)
	}
}
