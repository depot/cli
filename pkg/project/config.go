package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

type ProjectConfig struct {
	ID string `json:"id" yaml:"id"`
}

func ReadConfig(cwd string) (*ProjectConfig, string, error) {
	filename, err := FindConfigFileUp(cwd)
	if err != nil {
		return nil, "", err
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, "", err
	}

	var config ProjectConfig
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, "", err
	}

	return &config, filename, nil
}

func WriteConfig(filename string, config *ProjectConfig) error {
	ext := filepath.Ext(filename)
	var data []byte
	var err error

	switch ext {
	case ".json":
		data, err = json.Marshal(config)
		if err != nil {
			return err
		}

	case ".yaml", ".yml":
		data, err = yaml.Marshal(config)
		if err != nil {
			return err
		}

	default:
		return fmt.Errorf("Unsupported config file extension: %s", ext)
	}

	return os.WriteFile(filename, data, 0644)
}

func FindConfigFileUp(current string) (string, error) {
	for {
		path := filepath.Join(current, "depot.json")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		path = filepath.Join(current, "depot.yml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		path = filepath.Join(current, "depot.yaml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}
	return "", fmt.Errorf("No project config found")
}
