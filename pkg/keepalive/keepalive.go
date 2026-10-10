package keepalive

import (
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/keepalive"
)

func ClientParameters() keepalive.ClientParameters {
	return keepalive.ClientParameters{
		Time:                durationMilliseconds("DEPOT_KEEPALIVE_CLIENT_TIME_MS"),
		Timeout:             durationMilliseconds("DEPOT_KEEPALIVE_CLIENT_TIMEOUT_MS"),
		PermitWithoutStream: boolean("DEPOT_KEEPALIVE_CLIENT_PERMIT_WITHOUT_STREAM"),
	}
}

func ServerParameters() keepalive.ServerParameters {
	return keepalive.ServerParameters{
		MaxConnectionIdle:     durationMilliseconds("DEPOT_KEEPALIVE_SERVER_MAX_CONN_IDLE_MS"),
		MaxConnectionAge:      durationMilliseconds("DEPOT_KEEPALIVE_SERVER_MAX_CONN_AGE_MS"),
		MaxConnectionAgeGrace: durationMilliseconds("DEPOT_KEEPALIVE_SERVER_MAX_CONN_AGE_GRACE_MS"),
		Time:                  durationMilliseconds("DEPOT_KEEPALIVE_SERVER_TIME_MS"),
		Timeout:               durationMilliseconds("DEPOT_KEEPALIVE_SERVER_TIMEOUT_MS"),
	}
}

func EnforcementPolicy() keepalive.EnforcementPolicy {
	return keepalive.EnforcementPolicy{
		MinTime:             durationMilliseconds("DEPOT_KEEPALIVE_SERVER_POLICY_MINTIME_MS"),
		PermitWithoutStream: boolean("DEPOT_KEEPALIVE_SERVER_POLICY_PERMIT_WITHOUT_STREAM"),
	}
}

func durationMilliseconds(name string) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return 0
	}
	d, err := time.ParseDuration(value + "ms")
	if err != nil {
		logrus.Infof("couldn't parse %s value: %s", name, err)
		return 0
	}
	return d
}

func boolean(name string) bool {
	value := os.Getenv(name)
	if value == "" {
		return false
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		logrus.Infof("couldn't parse %s value: %s", name, err)
		return false
	}
	return b
}
