package authServer

import (
	api "authd/pkg/grpc/auth"

	"google.golang.org/grpc"
)

func NewServer(authServer api.AuthAPIServer) *grpc.Server {
	server := grpc.NewServer()
	api.RegisterAuthAPIServer(server, authServer)
	return server
}
