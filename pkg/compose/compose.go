package compose

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/compose-spec/compose-go/v2/consts"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	compose "github.com/compose-spec/compose-go/v2/types"
	"github.com/depot/cli/pkg/buildx/bake"
	"gopkg.in/yaml.v2"
)

type xbake struct {
	Tags []string `yaml:"tags,omitempty"`
}

// TODO: largely copied from buildx/bake/bake.go.  Refactor in buildx fork.
func TargetTags(files []bake.File) (map[string][]string, error) {
	if len(files) == 0 {
		return nil, errors.New("no files")
	}
	var configFiles []compose.ConfigFile

	for _, file := range files {
		if isComposeFile(file.Name, file.Data) {
			configFiles = append(configFiles, compose.ConfigFile{
				Filename: file.Name,
				Content:  file.Data,
			})
		}
	}

	if len(configFiles) == 0 {
		return nil, nil
	}

	envs, err := composeEnv()
	if err != nil {
		return nil, err
	}

	details := compose.ConfigDetails{
		ConfigFiles: configFiles,
		Environment: envs,
	}
	opts := func(options *loader.Options) {
		if nameFromEnv, ok := envs[consts.ComposeProjectName]; ok && nameFromEnv != "" {
			options.SetProjectName(nameFromEnv, true)
		} else {
			path, err := filepath.Abs(files[0].Name)
			if err != nil {
				return
			}
			absWorkingDir := filepath.Dir(path)
			options.SetProjectName(
				loader.NormalizeProjectName(filepath.Base(absWorkingDir)),
				false,
			)
		}
		options.SkipNormalization = true
	}

	cfg, err := loader.Load(details, opts)
	if err != nil {
		return nil, err
	}

	projectName := cfg.Name
	if projectName == "" {
		path, err := filepath.Abs(files[0].Name)
		if err != nil {
			return nil, err
		}
		dir := filepath.Base(filepath.Dir(path))
		if dir != "." {
			projectName = dir
		}
	}

	targetTags := map[string][]string{}
	for _, srv := range cfg.Services {
		if srv.Build == nil {
			continue
		}

		target := strings.ReplaceAll(srv.Name, ".", "_")
		if len(srv.Build.Tags) > 0 {
			targetTags[target] = srv.Build.Tags
		} else {
			if bakeExtension, ok := srv.Build.Extensions["x-bake"]; ok {
				var xb xbake
				yb, _ := yaml.Marshal(bakeExtension)
				err := yaml.Unmarshal(yb, &xb)
				if err == nil && len(xb.Tags) > 0 {
					targetTags[target] = xb.Tags
					continue
				}
			}

			imageNames := []string{getImageNameOrDefault(srv, projectName)}
			targetTags[target] = imageNames
		}
	}

	return targetTags, nil
}

// getImageNameOrDefault computes the default image name for a service, used to tag built images
func getImageNameOrDefault(service types.ServiceConfig, projectName string) string {
	imageName := service.Image
	if imageName == "" {
		r := regexp.MustCompile("[a-z0-9_-]")
		projectName = strings.ToLower(projectName)
		projectName = strings.Join(r.FindAllString(projectName, -1), "")
		projectName = strings.TrimLeft(projectName, "_-")

		imageName = projectName + "-" + service.Name
	}
	return imageName
}

func isComposeFile(file string, content []byte) bool {
	envs, err := composeEnv()
	if err != nil {
		return false
	}

	file = strings.ToLower(file)
	if strings.HasSuffix(file, ".json") || strings.HasSuffix(file, ".hcl") {
		return false
	}

	config := compose.ConfigDetails{
		ConfigFiles: []compose.ConfigFile{{
			Content: content,
		}},
		Environment: envs,
	}

	opts := func(options *loader.Options) {
		projectName := "bake"
		if v, ok := envs[consts.ComposeProjectName]; ok && v != "" {
			projectName = v
		}
		options.SetProjectName(projectName, false)
		options.SkipNormalization = true
		options.SkipConsistencyCheck = true
	}

	_, err = loader.Load(config, opts)
	if err != nil {
		return false
	}
	return err == nil
}

func composeEnv() (map[string]string, error) {
	envs := sliceToMap(os.Environ())
	if wd, err := os.Getwd(); err == nil {
		envs, err = loadDotEnv(envs, wd)
		if err != nil {
			return nil, err
		}
	}
	return envs, nil
}

func sliceToMap(env []string) (res map[string]string) {
	res = make(map[string]string)
	for _, s := range env {
		kv := strings.SplitN(s, "=", 2)
		key := kv[0]
		switch {
		case len(kv) == 1:
			res[key] = ""
		default:
			res[key] = kv[1]
		}
	}
	return
}

func loadDotEnv(curenv map[string]string, workingDir string) (map[string]string, error) {
	if curenv == nil {
		curenv = make(map[string]string)
	}

	ef, err := filepath.Abs(filepath.Join(workingDir, ".env"))
	if err != nil {
		return nil, err
	}

	if _, err = os.Stat(ef); os.IsNotExist(err) {
		return curenv, nil
	} else if err != nil {
		return nil, err
	}

	dt, err := os.ReadFile(ef)
	if err != nil {
		return nil, err
	}

	envs, err := dotenv.UnmarshalBytesWithLookup(dt, nil)
	if err != nil {
		return nil, err
	}

	for k, v := range envs {
		if _, set := curenv[k]; set {
			continue
		}
		curenv[k] = v
	}

	return curenv, nil
}
