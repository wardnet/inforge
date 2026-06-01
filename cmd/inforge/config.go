package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type projectConfig struct {
	Name    string `yaml:"name"`
	Backend struct {
		URL string `yaml:"url"`
	} `yaml:"backend"`
}

type stackConfig struct {
	Config map[string]string `yaml:"config"`
}

func loadProjectConfig(path string) (projectConfig, error) {
	var cfg projectConfig
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("inforge.yaml not found — run from the repo root or pass --config")
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Name == "" {
		return cfg, fmt.Errorf("%s: name is required", path)
	}
	return cfg, nil
}

func loadStackConfig(path string) (stackConfig, error) {
	var cfg stackConfig
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("stack config not found: %s", path)
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}
