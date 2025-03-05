package buildflags

import (
	"encoding/csv"
	"encoding/json"
	"strings"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/pkg/errors"
)

type Secrets []*Secret

func (s Secrets) Merge(other Secrets) Secrets {
	if other == nil {
		s.Normalize()
		return s
	} else if s == nil {
		other.Normalize()
		return other
	}

	return append(s, other...).Normalize()
}

func (s Secrets) Normalize() Secrets {
	if len(s) == 0 {
		return nil
	}
	return removeDupes(s)
}

type Secret struct {
	ID       string `json:"id,omitempty"`
	FilePath string `json:"src,omitempty"`
	Env      string `json:"env,omitempty"`
}

func (s *Secret) Equal(other *Secret) bool {
	return s.ID == other.ID && s.FilePath == other.FilePath && s.Env == other.Env
}

func (s *Secret) String() string {
	var b csvBuilder
	if s.ID != "" {
		b.Write("id", s.ID)
	}
	if s.FilePath != "" {
		b.Write("src", s.FilePath)
	}
	if s.Env != "" {
		b.Write("env", s.Env)
	}
	return b.String()
}

func (s *Secret) UnmarshalJSON(data []byte) error {
	var v struct {
		ID       string `json:"id,omitempty"`
		FilePath string `json:"src,omitempty"`
		Env      string `json:"env,omitempty"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	s.ID = v.ID
	s.FilePath = v.FilePath
	s.Env = v.Env
	return nil
}

func (s *Secret) UnmarshalText(text []byte) error {
	value := string(text)
	csvReader := csv.NewReader(strings.NewReader(value))
	fields, err := csvReader.Read()
	if err != nil {
		return errors.Wrap(err, "failed to parse csv secret")
	}

	*s = Secret{}

	var typ string
	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		key := strings.ToLower(parts[0])

		if len(parts) != 2 {
			return errors.Errorf("invalid field '%s' must be a key=value pair", field)
		}

		value := parts[1]
		switch key {
		case "type":
			if value != "file" && value != "env" {
				return errors.Errorf("unsupported secret type %q", value)
			}
			typ = value
		case "id":
			s.ID = value
		case "source", "src":
			s.FilePath = value
		case "env":
			s.Env = value
		default:
			return errors.Errorf("unexpected key '%s' in '%s'", key, field)
		}
	}
	if typ == "env" && s.Env == "" {
		s.Env = s.FilePath
		s.FilePath = ""
	}
	return nil
}

func CreateSecrets(secrets []*Secret) (session.Attachable, error) {
	fs := make([]secretsprovider.Source, 0, len(secrets))
	for _, secret := range secrets {
		fs = append(fs, secretsprovider.Source{
			ID:       secret.ID,
			FilePath: secret.FilePath,
			Env:      secret.Env,
		})
	}
	store, err := secretsprovider.NewStore(fs)
	if err != nil {
		return nil, err
	}
	return secretsprovider.NewSecretProvider(store), nil
}
