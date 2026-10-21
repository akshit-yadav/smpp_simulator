// Package gateway defines a gateway's configuration as the worker receives it
// and loads it from a JSON file (until the control plane serves it in M3).
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/outcome"
)

// Secret holds a password. It never prints or logs its value.
type Secret string

func (Secret) String() string        { return "*****" }
func (Secret) GoString() string      { return "*****" }
func (Secret) LogValue() slog.Value  { return slog.StringValue("*****") }
func (s Secret) Equal(o Secret) bool { return s == o }

const (
	StatusActive    = "active"
	StatusDisabled  = "disabled"
	StatusSuspended = "suspended" // by licence

	BindTX  = "tx"
	BindRX  = "rx"
	BindTRX = "trx"

	TPSPerBind    = "bind"
	TPSPerGateway = "gateway"

	TimingImmediate = "immediate"
	TimingFixed     = "fixed"
	TimingRandom    = "random"

	IDHex     = "hex"
	IDDecimal = "decimal"
)

type TPS struct {
	Limit float64 `json:"limit"` // 0 = unlimited
	Scope string  `json:"scope"` // bind | gateway
}

type DLR struct {
	Enabled     bool   `json:"enabled"`
	Timing      string `json:"timing"`   // immediate | fixed | random
	DelayMS     int    `json:"delay_ms"` // fixed
	MinMS       int    `json:"min_ms"`   // random
	MaxMS       int    `json:"max_ms"`
	IDFormat    string `json:"id_format"`    // hex | decimal
	DateSeconds bool   `json:"date_seconds"` // YYMMDDhhmmss instead of YYMMDDhhmm
	IncludeText bool   `json:"include_text"`
	IncludeTLVs bool   `json:"include_tlvs"`
}

type Config struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Port      int             `json:"port"`
	SystemID  string          `json:"system_id"`
	Password  Secret          `json:"password"`
	Status    string          `json:"status"`
	MaxBinds  int             `json:"max_binds"` // 0 = unlimited
	BindTypes []string        `json:"bind_types"`
	TPS       TPS             `json:"tps"`
	DLR       DLR             `json:"dlr"`
	Outcome   outcome.Profile `json:"outcome"`
	Version   int64           `json:"version"`
}

// Builtin are the defaults every gateway starts from.
var Builtin = Config{
	Status:    StatusActive,
	MaxBinds:  10,
	BindTypes: []string{BindTX, BindRX, BindTRX},
	TPS:       TPS{Limit: 0, Scope: TPSPerBind},
	DLR: DLR{
		Enabled: true, Timing: TimingRandom, MinMS: 500, MaxMS: 2000,
		IDFormat: IDHex, DateSeconds: false, IncludeText: true, IncludeTLVs: true,
	},
	Outcome: outcome.Default,
}

// AllowsBind reports whether bind type t ("tx", "rx", "trx") is allowed.
func (c *Config) AllowsBind(t string) bool { return slices.Contains(c.BindTypes, t) }

func (c *Config) Validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(c.ID > 0, "id must be positive")
	check(c.Name != "", "name is required")
	check(c.Port > 0 && c.Port < 65536, "invalid port %d", c.Port)
	check(c.SystemID != "" && len(c.SystemID) <= 15, "system_id must be 1-15 characters")
	check(len(c.Password) <= 8, "password must be at most 8 characters")
	check(slices.Contains([]string{StatusActive, StatusDisabled, StatusSuspended}, c.Status), "invalid status %q", c.Status)
	check(c.MaxBinds >= 0, "max_binds must be >= 0")
	check(len(c.BindTypes) > 0, "bind_types must not be empty")
	for _, t := range c.BindTypes {
		check(t == BindTX || t == BindRX || t == BindTRX, "invalid bind type %q", t)
	}
	check(c.TPS.Limit >= 0, "tps.limit must be >= 0")
	check(c.TPS.Scope == TPSPerBind || c.TPS.Scope == TPSPerGateway, "invalid tps.scope %q", c.TPS.Scope)
	switch c.DLR.Timing {
	case TimingImmediate:
	case TimingFixed:
		check(c.DLR.DelayMS >= 0, "dlr.delay_ms must be >= 0")
	case TimingRandom:
		check(c.DLR.MinMS >= 0 && c.DLR.MaxMS >= c.DLR.MinMS, "dlr.min_ms/max_ms invalid")
	default:
		check(false, "invalid dlr.timing %q", c.DLR.Timing)
	}
	check(c.DLR.IDFormat == IDHex || c.DLR.IDFormat == IDDecimal, "invalid dlr.id_format %q", c.DLR.IDFormat)
	if err := c.Outcome.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("outcome: %w", err))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("gateway %d (%s): %w", c.ID, c.Name, err)
	}
	return nil
}

// File is the static gateway file: shared defaults plus gateways that
// override any of them. A field left out of a gateway follows the default.
type File struct {
	Defaults json.RawMessage   `json:"defaults"`
	Gateways []json.RawMessage `json:"gateways"`
}

// LoadFile reads and validates a gateway file.
func LoadFile(path string) ([]Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes a gateway file. Each gateway is unmarshalled on top of a copy
// of the defaults, so JSON fields it omits keep the default value.
func Parse(data []byte) ([]Config, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	base := Builtin
	if len(f.Defaults) > 0 {
		if err := json.Unmarshal(f.Defaults, &base); err != nil {
			return nil, fmt.Errorf("defaults: %w", err)
		}
	}
	baseJSON, _ := json.Marshal(base)

	var out []Config
	ids, ports, names := map[int64]bool{}, map[int]bool{}, map[string]bool{}
	for i, raw := range f.Gateways {
		var c Config
		_ = json.Unmarshal(baseJSON, &c) // deep copy of the defaults
		c.Password = base.Password
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("gateways[%d]: %w", i, err)
		}
		if err := c.Validate(); err != nil {
			return nil, err
		}
		if ids[c.ID] || ports[c.Port] || names[c.Name] {
			return nil, fmt.Errorf("gateway %d (%s): duplicate id, port or name", c.ID, c.Name)
		}
		ids[c.ID], ports[c.Port], names[c.Name] = true, true, true
		out = append(out, c)
	}
	return out, nil
}
