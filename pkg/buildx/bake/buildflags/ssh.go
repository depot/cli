package buildflags

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/util/gitutil"
)

type SSHKeys []*SSH

func (s SSHKeys) Merge(other SSHKeys) SSHKeys {
	if other == nil {
		s.Normalize()
		return s
	} else if s == nil {
		other.Normalize()
		return other
	}

	return append(s, other...).Normalize()
}

func (s SSHKeys) Normalize() SSHKeys {
	if len(s) == 0 {
		return nil
	}
	return removeDupes(s)
}

type SSH struct {
	ID    string   `json:"id,omitempty" cty:"id"`
	Paths []string `json:"paths,omitempty" cty:"paths"`
}

func (s *SSH) Equal(other *SSH) bool {
	return s.Less(other) == 0
}

func (s *SSH) Less(other *SSH) int {
	if s.ID != other.ID {
		return cmp.Compare(s.ID, other.ID)
	}
	return slices.Compare(s.Paths, other.Paths)
}

func (s *SSH) String() string {
	if len(s.Paths) == 0 {
		return s.ID
	}

	var b csvBuilder
	paths := strings.Join(s.Paths, ",")
	b.Write(s.ID, paths)
	return b.String()
}

func (s *SSH) UnmarshalJSON(data []byte) error {
	var v struct {
		ID    string   `json:"id,omitempty"`
		Paths []string `json:"paths,omitempty"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	s.ID = v.ID
	s.Paths = v.Paths
	return nil
}

func (s *SSH) UnmarshalText(text []byte) error {
	parts := strings.SplitN(string(text), "=", 2)

	s.ID = parts[0]
	if len(parts) > 1 {
		s.Paths = strings.Split(parts[1], ",")
	} else {
		s.Paths = nil
	}
	return nil
}

// IsGitSSH returns true if the given repo URL is accessed over ssh
func IsGitSSH(url string) bool {
	_, gitProtocol := gitutil.ParseProtocol(url)
	return gitProtocol == gitutil.SSHProtocol
}

func CreateSSH(ssh []*SSH) (session.Attachable, error) {
	configs := make([]sshprovider.AgentConfig, 0, len(ssh))
	for _, ssh := range ssh {
		cfg := sshprovider.AgentConfig{
			ID:    ssh.ID,
			Paths: append([]string{}, ssh.Paths...),
		}
		configs = append(configs, cfg)
	}
	return sshprovider.NewSSHAgentProvider(configs)
}
