package core

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"lingobridge/internal/config"
	"lingobridge/internal/store"
)

// ErrPlatformConfigNotFound is returned when a scoped platform config is absent.
var ErrPlatformConfigNotFound = errors.New("platform config not found")

// PlatformConfigAPI is the config persistence API exposed to platform adapters.
type PlatformConfigAPI interface {
	PlatformID() string
	GetPlatformConfig(platformID string, out any) error
	SetPlatformConfig(platformID string, value any) error
	UpdatePlatformConfig(platformID string, fn func(node *yaml.Node) error) error
}

// PlatformContext scopes config and data access to one platform.
type PlatformContext struct {
	platformID string
	cfg        *config.Config
	store      *store.Store
	saveConfig func(config.Config) error
}

// NewPlatformContext creates a platform-scoped persistence context.
func NewPlatformContext(platformID string, cfg *config.Config, st *store.Store, saveConfig func(config.Config) error) (*PlatformContext, error) {
	if err := config.ValidatePlatformID(platformID); err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if st != nil && st.PlatformID() != platformID {
		return nil, fmt.Errorf("store platform %q does not match context platform %q", st.PlatformID(), platformID)
	}
	if cfg.Platforms == nil {
		cfg.Platforms = map[string]yaml.Node{}
	}
	return &PlatformContext{
		platformID: platformID,
		cfg:        cfg,
		store:      st,
		saveConfig: saveConfig,
	}, nil
}

// PlatformID returns the platform this context is scoped to.
func (c *PlatformContext) PlatformID() string {
	return c.platformID
}

// DataStore returns the platform-scoped data store.
func (c *PlatformContext) DataStore() *store.Store {
	return c.store
}

// GetPlatformConfig decodes a platform config node into out.
func (c *PlatformContext) GetPlatformConfig(platformID string, out any) error {
	if err := c.requirePlatform(platformID); err != nil {
		return err
	}
	node, ok := c.cfg.Platforms[platformID]
	if !ok || node.Kind == 0 {
		return fmt.Errorf("%w: %s", ErrPlatformConfigNotFound, platformID)
	}
	if out == nil {
		return nil
	}
	if err := node.Decode(out); err != nil {
		return fmt.Errorf("decode %s platform config: %w", platformID, err)
	}
	return nil
}

// SetPlatformConfig replaces a platform config node and persists the shared config.
func (c *PlatformContext) SetPlatformConfig(platformID string, value any) error {
	if err := c.requirePlatform(platformID); err != nil {
		return err
	}
	node, err := valueToYAMLNode(value)
	if err != nil {
		return fmt.Errorf("encode %s platform config: %w", platformID, err)
	}
	c.cfg.Platforms[platformID] = node
	return c.persist()
}

// UpdatePlatformConfig mutates a platform config node and persists the shared config.
func (c *PlatformContext) UpdatePlatformConfig(platformID string, fn func(node *yaml.Node) error) error {
	if err := c.requirePlatform(platformID); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("platform config updater is required")
	}
	node, ok := c.cfg.Platforms[platformID]
	if !ok || node.Kind == 0 {
		node = yaml.Node{Kind: yaml.MappingNode}
	}
	if err := fn(&node); err != nil {
		return err
	}
	c.cfg.Platforms[platformID] = node
	return c.persist()
}

func (c *PlatformContext) requirePlatform(platformID string) error {
	if platformID != c.platformID {
		return fmt.Errorf("platform config access denied: %q cannot access %q", c.platformID, platformID)
	}
	return nil
}

func (c *PlatformContext) persist() error {
	if c.saveConfig == nil {
		return nil
	}
	return c.saveConfig(*c.cfg)
}

func valueToYAMLNode(value any) (yaml.Node, error) {
	var doc yaml.Node
	data, err := yaml.Marshal(value)
	if err != nil {
		return yaml.Node{}, err
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return yaml.Node{}, err
	}
	if len(doc.Content) == 0 {
		return yaml.Node{Kind: yaml.MappingNode}, nil
	}
	return *doc.Content[0], nil
}
