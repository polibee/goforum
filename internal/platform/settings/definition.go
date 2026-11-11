package settings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type Type string

const (
	TypeString Type = "string"
	TypeText   Type = "text"
	TypeBool   Type = "bool"
	TypeInt    Type = "int"
	TypeEnum   Type = "enum"
	TypeJSON   Type = "json"
	TypeSecret Type = "secret"
	TypeColor  Type = "color"
	TypeURL    Type = "url"
	TypeEmail  Type = "email"
)

type Definition struct {
	Namespace   string
	Key         string
	Group       string
	Label       string
	Type        Type
	Default     any
	Enum        []string
	EnumLabels  map[string]string
	Min         int
	Max         int
	Permission  string
	Restart     bool
	Secret      bool
	Description string
	// RegisterURL renders a provider sign-up link next to the field.
	RegisterURL string
	// Hidden keeps a setting out of the generic settings page. It is still
	// registered, validated and saved; a dedicated admin screen owns the UI.
	Hidden bool
}

func (d Definition) FullKey() string { return d.Namespace + "." + d.Key }

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (d Definition) Validate() error {
	full := d.FullKey()
	if !keyPattern.MatchString(full) {
		return fmt.Errorf("invalid setting key %q", full)
	}
	if d.Group == "" {
		return fmt.Errorf("setting %s: group is required", full)
	}
	if d.Label == "" {
		return fmt.Errorf("setting %s: label is required", full)
	}
	switch d.Type {
	case TypeString, TypeText, TypeBool, TypeInt, TypeEnum, TypeJSON, TypeSecret, TypeColor, TypeURL, TypeEmail:
	default:
		return fmt.Errorf("setting %s: unsupported type %q", full, d.Type)
	}
	if _, err := d.normalize(d.Default); err != nil {
		return fmt.Errorf("setting %s default: %w", full, err)
	}
	return nil
}

func (d Definition) normalize(v any) (any, error) {
	switch d.Type {
	case TypeString, TypeText, TypeSecret, TypeURL, TypeEmail, TypeColor, TypeEnum:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected string, got %T", v)
		}
		s = strings.TrimSpace(s)
		switch d.Type {
		case TypeColor:
			if s != "" && !colorPattern.MatchString(s) {
				return nil, fmt.Errorf("invalid color %q (want #RRGGBB)", s)
			}
		case TypeURL:
			if s != "" {
				u, err := url.Parse(s)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
					return nil, fmt.Errorf("invalid url %q", s)
				}
			}
		case TypeEmail:
			if s != "" && !strings.Contains(s, "@") {
				return nil, fmt.Errorf("invalid email %q", s)
			}
		case TypeEnum:
			if len(d.Enum) > 0 && !contains(d.Enum, s) {
				return nil, fmt.Errorf("value %q not in %v", s, d.Enum)
			}
		}
		if d.Max > 0 && len([]rune(s)) > d.Max {
			return nil, fmt.Errorf("length exceeds %d", d.Max)
		}
		return s, nil
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("expected bool, got %T", v)
		}
		return b, nil
	case TypeInt:
		var n int64
		switch x := v.(type) {
		case int:
			n = int64(x)
		case int64:
			n = x
		case float64:
			n = int64(x)
		case json.Number:
			parsed, err := x.Int64()
			if err != nil {
				return nil, err
			}
			n = parsed
		default:
			return nil, fmt.Errorf("expected int, got %T", v)
		}
		if d.Min != 0 && n < int64(d.Min) {
			return nil, fmt.Errorf("must be >= %d", d.Min)
		}
		if d.Max != 0 && n > int64(d.Max) {
			return nil, fmt.Errorf("must be <= %d", d.Max)
		}
		return n, nil
	case TypeJSON:
		if _, err := json.Marshal(v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("unsupported type %q", d.Type)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
