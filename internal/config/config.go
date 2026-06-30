// Package config loads boot/infra configuration via commons configloader
// (LLD §9): the values a process needs to start, with ${VAR:default} env
// expansion. Verticals and questions (Apollo configlib + in-memory cache) are
// a separate concern wired later.
package config

import "github.com/Bureau-Inc/bureau-commons-go/commons/configloader"

// Config holds boot/infra settings. Only the fields used today are present;
// Mongo URI, Auth0, and timeouts are added here as those peripherals are wired.
type Config struct {
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
	Environment string `yaml:"environment"`
}

// Load reads the YAML config at path, applying configloader's ${VAR:default}
// environment expansion before unmarshalling.
func Load(path string) (*Config, error) {
	var c Config
	if err := configloader.LoadYAML(path, &c); err != nil {
		return nil, err
	}
	if c.Server.Port == "" {
		c.Server.Port = "8080" // safety default if file/env yield an empty port
	}
	return &c, nil
}
