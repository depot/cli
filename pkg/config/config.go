package config

import (
	"fmt"

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

func SetApiToken(token string) error {
	viper.Set("api_token", token)
	return viper.WriteConfig()
}

func SetCurrentOrganization(orgId string) error {
	viper.Set("org_id", orgId)
	return viper.WriteConfig()
}

func ClearApiToken() error {
	viper.Set("api_token", "")
	return viper.WriteConfig()
}

func StateFile() (string, error) {
	return xdg.ConfigFile("depot/state.yaml")
}
