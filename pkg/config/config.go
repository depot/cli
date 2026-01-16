package config

import (
	"fmt"
	"os"

	"github.com/adrg/xdg"
	"github.com/spf13/viper"
)

func NewConfig() error {
	configPath, err := xdg.ConfigFile("depot/depot.yaml")
	if err != nil {
		return err
	}

	viper.SetConfigFile(configPath)
	viper.SetEnvPrefix("DEPOT")
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		// It's okay if the config file doesn't exist
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("unable to read config file: %v", err)
		}
	}
	return nil
}

func GetApiToken() string {
	return viper.GetString("api_token")
}

func GetCurrentOrganization() string {
	return viper.GetString("org_id")
}

func writeConfig() error {
	configFile := viper.ConfigFileUsed()
	if configFile == "" {
		return fmt.Errorf("no config file set")
	}

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		f, err := os.OpenFile(configFile, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		f.Close()
	} else if err == nil {
		if err := os.Chmod(configFile, 0600); err != nil {
			return err
		}
	}

	return viper.WriteConfig()
}

func SetApiToken(token string) error {
	viper.Set("api_token", token)
	return writeConfig()
}

func SetCurrentOrganization(orgId string) error {
	viper.Set("org_id", orgId)
	return writeConfig()
}

func ClearApiToken() error {
	viper.Set("api_token", "")
	return writeConfig()
}

func ClearCurrentOrganization() error {
	viper.Set("org_id", "")
	return writeConfig()
}

func StateFile() (string, error) {
	return xdg.ConfigFile("depot/state.yaml")
}
