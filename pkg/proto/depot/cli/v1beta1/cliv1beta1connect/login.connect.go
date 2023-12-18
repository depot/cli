// Code generated by protoc-gen-connect-go. DO NOT EDIT.
//
// Source: depot/cli/v1beta1/login.proto

package cliv1beta1connect

import (
	connect "connectrpc.com/connect"
	context "context"
	errors "errors"
	v1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	http "net/http"
	strings "strings"
)

// This is a compile-time assertion to ensure that this generated file and the connect package are
// compatible. If you get a compiler error that this constant is not defined, this code was
// generated with a version of connect newer than the one compiled into your binary. You can fix the
// problem by either regenerating this code with an older version of connect or updating the connect
// version compiled into your binary.
const _ = connect.IsAtLeastVersion0_1_0

const (
	// LoginServiceName is the fully-qualified name of the LoginService service.
	LoginServiceName = "depot.cli.v1beta1.LoginService"
)

// These constants are the fully-qualified names of the RPCs defined in this package. They're
// exposed at runtime as Spec.Procedure and as the final two segments of the HTTP route.
//
// Note that these are different from the fully-qualified method names used by
// google.golang.org/protobuf/reflect/protoreflect. To convert from these constants to
// reflection-formatted method names, remove the leading slash and convert the remaining slash to a
// period.
const (
	// LoginServiceStartLoginProcedure is the fully-qualified name of the LoginService's StartLogin RPC.
	LoginServiceStartLoginProcedure = "/depot.cli.v1beta1.LoginService/StartLogin"
	// LoginServiceFinishLoginProcedure is the fully-qualified name of the LoginService's FinishLogin
	// RPC.
	LoginServiceFinishLoginProcedure = "/depot.cli.v1beta1.LoginService/FinishLogin"
)

// LoginServiceClient is a client for the depot.cli.v1beta1.LoginService service.
type LoginServiceClient interface {
	StartLogin(context.Context, *connect.Request[v1beta1.StartLoginRequest]) (*connect.Response[v1beta1.StartLoginResponse], error)
	FinishLogin(context.Context, *connect.Request[v1beta1.FinishLoginRequest]) (*connect.ServerStreamForClient[v1beta1.FinishLoginResponse], error)
}

// NewLoginServiceClient constructs a client for the depot.cli.v1beta1.LoginService service. By
// default, it uses the Connect protocol with the binary Protobuf Codec, asks for gzipped responses,
// and sends uncompressed requests. To use the gRPC or gRPC-Web protocols, supply the
// connect.WithGRPC() or connect.WithGRPCWeb() options.
//
// The URL supplied here should be the base URL for the Connect or gRPC server (for example,
// http://api.acme.com or https://acme.com/grpc).
func NewLoginServiceClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) LoginServiceClient {
	baseURL = strings.TrimRight(baseURL, "/")
	return &loginServiceClient{
		startLogin: connect.NewClient[v1beta1.StartLoginRequest, v1beta1.StartLoginResponse](
			httpClient,
			baseURL+LoginServiceStartLoginProcedure,
			opts...,
		),
		finishLogin: connect.NewClient[v1beta1.FinishLoginRequest, v1beta1.FinishLoginResponse](
			httpClient,
			baseURL+LoginServiceFinishLoginProcedure,
			opts...,
		),
	}
}

// loginServiceClient implements LoginServiceClient.
type loginServiceClient struct {
	startLogin  *connect.Client[v1beta1.StartLoginRequest, v1beta1.StartLoginResponse]
	finishLogin *connect.Client[v1beta1.FinishLoginRequest, v1beta1.FinishLoginResponse]
}

// StartLogin calls depot.cli.v1beta1.LoginService.StartLogin.
func (c *loginServiceClient) StartLogin(ctx context.Context, req *connect.Request[v1beta1.StartLoginRequest]) (*connect.Response[v1beta1.StartLoginResponse], error) {
	return c.startLogin.CallUnary(ctx, req)
}

// FinishLogin calls depot.cli.v1beta1.LoginService.FinishLogin.
func (c *loginServiceClient) FinishLogin(ctx context.Context, req *connect.Request[v1beta1.FinishLoginRequest]) (*connect.ServerStreamForClient[v1beta1.FinishLoginResponse], error) {
	return c.finishLogin.CallServerStream(ctx, req)
}

// LoginServiceHandler is an implementation of the depot.cli.v1beta1.LoginService service.
type LoginServiceHandler interface {
	StartLogin(context.Context, *connect.Request[v1beta1.StartLoginRequest]) (*connect.Response[v1beta1.StartLoginResponse], error)
	FinishLogin(context.Context, *connect.Request[v1beta1.FinishLoginRequest], *connect.ServerStream[v1beta1.FinishLoginResponse]) error
}

// NewLoginServiceHandler builds an HTTP handler from the service implementation. It returns the
// path on which to mount the handler and the handler itself.
//
// By default, handlers support the Connect, gRPC, and gRPC-Web protocols with the binary Protobuf
// and JSON codecs. They also support gzip compression.
func NewLoginServiceHandler(svc LoginServiceHandler, opts ...connect.HandlerOption) (string, http.Handler) {
	loginServiceStartLoginHandler := connect.NewUnaryHandler(
		LoginServiceStartLoginProcedure,
		svc.StartLogin,
		opts...,
	)
	loginServiceFinishLoginHandler := connect.NewServerStreamHandler(
		LoginServiceFinishLoginProcedure,
		svc.FinishLogin,
		opts...,
	)
	return "/depot.cli.v1beta1.LoginService/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LoginServiceStartLoginProcedure:
			loginServiceStartLoginHandler.ServeHTTP(w, r)
		case LoginServiceFinishLoginProcedure:
			loginServiceFinishLoginHandler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// UnimplementedLoginServiceHandler returns CodeUnimplemented from all methods.
type UnimplementedLoginServiceHandler struct{}

func (UnimplementedLoginServiceHandler) StartLogin(context.Context, *connect.Request[v1beta1.StartLoginRequest]) (*connect.Response[v1beta1.StartLoginResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("depot.cli.v1beta1.LoginService.StartLogin is not implemented"))
}

func (UnimplementedLoginServiceHandler) FinishLogin(context.Context, *connect.Request[v1beta1.FinishLoginRequest], *connect.ServerStream[v1beta1.FinishLoginResponse]) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("depot.cli.v1beta1.LoginService.FinishLogin is not implemented"))
}
