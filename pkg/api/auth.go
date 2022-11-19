package api

import (
	"context"
	"fmt"
	"time"

	"github.com/briandowns/spinner"
	"github.com/bufbuild/connect-go"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
)

func AuthorizeDevice(ctx context.Context) (*cliv1beta1.FinishLoginResponse, error) {
	client := NewLoginClient()
	req := cliv1beta1.StartLoginRequest{}
	response, err := client.StartLogin(ctx, WithHeaders(connect.NewRequest(&req), ""))
	if err != nil {
		return nil, err
	}
	fmt.Printf("Please visit the following URL in your browser to authenticate the CLI:\n\n    %s\n\n", response.Msg.ApproveUrl)

	spinner := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
	spinner.Prefix = "Waiting for approval "
	spinner.Start()
	defer spinner.Stop()

	stream, err := client.FinishLogin(ctx, WithHeaders(connect.NewRequest(&cliv1beta1.FinishLoginRequest{
		Id: response.Msg.Id,
	}), ""))
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	for stream.Receive() {
		response := stream.Msg()
		stream.Close()
		return response, nil
	}

	if err := stream.Err(); err != nil {
		return nil, connect.NewError(connect.CodeUnknown, err)
	}

	return nil, connect.NewError(connect.CodeUnknown, fmt.Errorf("unknown error"))
}
