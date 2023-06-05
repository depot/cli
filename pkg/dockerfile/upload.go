package dockerfile

import (
	"context"
	"time"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/buildx/build"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/docker/buildx/util/progress"
)

var _ build.DockerfileCallback = (*Uploader)(nil)

type DockerUpload struct {
	// Bake or Build target name.  If build then it is "default."
	Target string
	// Filename is the name of the Dockerfile.
	Filename string
	// Contents is the in-memory copy of the Dockerfile.
	Contents string
	// Reported is set to true if the Dockerfile has been reported to the server.
	Reported bool
}

func (d *DockerUpload) String() string {
	return d.Target + d.Filename + d.Contents
}

// Uploader sends context to the API.
type Uploader struct {
	buildID       string
	token         string
	client        cliv1connect.BuildServiceClient
	dockerUploads chan *DockerUpload
}

func NewUploader(buildID, token string) *Uploader {
	// Buffer up to 128 docker files before blocking the build.
	const channelBufferSize = 128
	return &Uploader{
		buildID:       buildID,
		token:         token,
		client:        depotapi.NewBuildClient(),
		dockerUploads: make(chan *DockerUpload, channelBufferSize),
	}
}

// Handle receives the Dockerfiles and buffers them to return to the build as quickly as possible.
func (u *Uploader) Handle(ctx context.Context, target string, driverIndex int, dockerfile *build.DockerfileInputs, printer progress.Writer) error {
	dockerUpload := &DockerUpload{
		Target:   target,
		Filename: dockerfile.Filename,
		Contents: string(dockerfile.Content),
	}
	select {
	case u.dockerUploads <- dockerUpload:
	default:
		// if channel is full skip buffer to prevent blocking the build.
	}

	return nil
}

// Run should be started in a go routine to send build Dockerfiles to the server on a timer.
//
// Cancel the context to stop the go routine.
func (u *Uploader) Run(ctx context.Context) {
	// Buffer 1 second before sending build dockerfiles to the server
	const (
		bufferTimeout = time.Second
	)

	ticker := time.NewTicker(bufferTimeout)
	defer ticker.Stop()

	uniqueDockerfiles := map[string]struct{}{}
	dockerUploads := []*DockerUpload{}

	for {
		select {
		case dockerUpload := <-u.dockerUploads:
			// Only record unique target/dockerfiles as we can receive duplicates with multi-platform builds.
			if _, ok := uniqueDockerfiles[dockerUpload.String()]; !ok {
				uniqueDockerfiles[dockerUpload.String()] = struct{}{}
				dockerUploads = append(dockerUploads, dockerUpload)
			}
		case <-ticker.C:
			// Requires a new context because the previous one may be canceled while we are
			// sending the files.  At most one will wait 5 seconds.
			ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			u.ReportDockerfiles(ctx2, dockerUploads)
			ticker.Reset(bufferTimeout)
			cancel()
		case <-ctx.Done():
			// Send all remaining files before exiting.
			for {
				select {
				case dockerUpload := <-u.dockerUploads:
					if _, ok := uniqueDockerfiles[dockerUpload.String()]; !ok {
						uniqueDockerfiles[dockerUpload.String()] = struct{}{}
						dockerUploads = append(dockerUploads, dockerUpload)
					}
				default:
					// Requires a new context because the previous one was canceled.
					ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					u.ReportDockerfiles(ctx2, dockerUploads)
					cancel()

					return
				}
			}
		}
	}
}

func (u *Uploader) ReportDockerfiles(ctx context.Context, dockerUploads []*DockerUpload) {
	if len(dockerUploads) == 0 {
		return
	}

	files := make([]*cliv1.Dockerfile, 0, len(dockerUploads))
	for i := range dockerUploads {
		// Skip dockerfiles we have already reported.
		if dockerUploads[i].Reported {
			continue
		}

		files = append(files, &cliv1.Dockerfile{
			Target:   dockerUploads[i].Target,
			Filename: dockerUploads[i].Filename,
			Contents: string(dockerUploads[i].Contents),
		})
	}

	req := &cliv1.ReportBuildContextRequest{
		BuildId:     u.buildID,
		Dockerfiles: files,
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := u.client.ReportBuildContext(ctx, depotapi.WithAuthentication(connect.NewRequest(req), u.token))

	if err != nil {
		// No need to log errors to the user as it is fine if are not able to upload the context.
		return
	} else {
		// Mark the dockerfiles as reported if the request was successful.
		for i := range dockerUploads {
			dockerUploads[i].Reported = true
		}
	}
}
