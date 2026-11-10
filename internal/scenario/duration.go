package scenario

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// UnmarshalYAML decodes a Go duration string such as "30s", "1m30s" or
// "250ms". Bare numbers ("30", 30) are rejected because the unit would be
// ambiguous.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a string like \"30s\"", node.Line)
	}
	if node.Tag == "!!int" || node.Tag == "!!float" {
		return fmt.Errorf("line %d: duration %q has no unit; use a value like \"%ss\" or \"%sms\"",
			node.Line, node.Value, node.Value, node.Value)
	}
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q: expected a Go duration such as \"30s\", \"1m30s\" or \"250ms\"",
			node.Line, node.Value)
	}
	d.Duration = v
	return nil
}

// MarshalYAML encodes the duration in its string form (e.g. "1m30s").
func (d Duration) MarshalYAML() (any, error) {
	return d.Duration.String(), nil
}
